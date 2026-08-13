package retrieval

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

// classifyActivationMatches builds the ActivationHits Trace needs from every
// LinkMatch Match() returned, and treats all of them as eligible for the
// fast path (2026-08-13, docs/impl/v1/activation.md「置信度分档判定」/
// docs/impl/v1/retrieval.md 步骤 2): Match() itself already decided, per
// condition, whether this round should serve — tiering (exploring/
// self_graded/trusted) and explore/audit sampling happened inside Match(),
// so a returned LinkMatch is always meant to be used this round. There's no
// more "verified-only" filter here — filtering by the link's stored status
// field would be filtering by a value that's now a lagging cache of the same
// per-condition confidence Match() just computed fresh.
func classifyActivationMatches(matches []activation.LinkMatch) ([]ActivationHit, []activation.LinkMatch, bool) {
	activationHits := make([]ActivationHit, len(matches))
	allLinkIDs := make([]string, len(matches))
	for i, m := range matches {
		activationHits[i] = ActivationHit{
			LinkID: m.Link.LinkID, PointID: m.Link.PointID, MatchScore: m.Score, MatchedBy: m.MatchedBy,
			Tier: string(m.Tier), AuditSampled: m.AuditSampled,
			Subject: m.Subject, Intent: m.Intent, Audience: m.Audience, Constraint: m.Constraint,
		}
		allLinkIDs[i] = m.Link.LinkID
	}
	slog.Info("retrieval: activation layer matched", "link_count", len(matches), "link_ids", allLinkIDs)

	if len(matches) == 0 {
		return activationHits, nil, false
	}
	return activationHits, matches, true
}

func verifiedIDs(verified []activation.LinkMatch) (linkIDs, pointIDs []string) {
	linkIDs = make([]string, len(verified))
	pointIDs = make([]string, len(verified))
	for i, m := range verified {
		linkIDs[i] = m.Link.LinkID
		pointIDs[i] = m.Link.PointID
	}
	return linkIDs, pointIDs
}

type unitResolutionStatus int

const (
	unitResolutionOK unitResolutionStatus = iota
	unitResolutionFailed
	unitResolutionAmbiguous
)

// resolveUnitsForPoints fetches current-KU hits for the matched verified
// links' point_ids. Single-unit is the common, unchanged case; multi-unit is
// reported as ambiguous so the caller can consult ActivationBundle before
// giving up on the fast path (docs/impl/v1/retrieval.md 步骤 2).
func (s *Service) resolveUnitsForPoints(pointIDs, linkIDs []string) ([]DirectHit, unitResolutionStatus) {
	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: fast path unit lookup failed, falling back to slow path", "error", err)
		return nil, unitResolutionFailed
	}
	if len(hits) == 0 {
		slog.Warn("retrieval: fast path found no current KU for matched links, falling back", "link_ids", linkIDs)
		return nil, unitResolutionFailed
	}
	unitSet := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		unitSet[h.UnitID] = struct{}{}
	}
	if len(unitSet) > 1 {
		return hits, unitResolutionAmbiguous
	}
	return hits, unitResolutionOK
}

// resolveBundleForAmbiguousHits implements docs/impl/v1/retrieval.md 步骤 2's
// Bundle-consultation branch: when verified ActivationLink matches span
// multiple KUs (raw ambiguity), consult ActivationBundle before giving up on
// the fast path. Only reached from resolveUnitsForPoints' ambiguous case —
// single-unit hits never call this, so that path stays pixel-identical to
// before.
func (s *Service) resolveBundleForAmbiguousHits(ctx context.Context, workQC QueryContext, expandedQuery session.ExpandedQuery, matchCfg activation.MatchConfig, linkIDs, pointIDs []string) ([]DirectHit, bool) {
	// 2026-08-13: no more Status==verified filter here, same reasoning as
	// classifyActivationMatches — MatchBundles' tiering already decided which
	// bundles are eligible to serve this round; trust its output set
	// directly (docs/impl/v1/activation.md「置信度分档判定」).
	verifiedBundles, err := s.activationSvc.MatchBundles(ctx, expandedQuery, matchCfg)
	if err != nil {
		slog.Warn("retrieval: bundle match failed, falling back to slow path", "error", err)
		return nil, false
	}

	if len(verifiedBundles) == 0 {
		// No verified Bundle covers this hit — form/enrich a candidate bundle
		// from this real-time observation (docs/impl/v1/activation-bundle.md
		// 步骤 4b), then fall back to slow path this round exactly as before;
		// bundle formation is a side effect, not a reason to change today's
		// control flow.
		s.formCandidateBundle(workQC, pointIDs)
		slog.Info("retrieval: activation matched verified links across multiple units, no verified bundle covers them, falling back to slow path",
			"link_ids", linkIDs, "point_ids", pointIDs)
		return nil, false
	}

	// 2026-08-13: 核心成员不再是建/刷新 Bundle 那一刻写死的静态数组，是
	// 每个成员自己的 mean(member) 越过 serving_confidence_min 派生出来的
	// 结果（docs/impl/v1/activation-bundle.md「成员置信度」组装时的用法）。
	confCfg := s.activationSvc.ConfidenceConfig()
	var resolvedPoints []string
	if len(verifiedBundles) == 1 {
		resolvedPoints = verifiedBundles[0].Bundle.CoreMemberPointIDs(confCfg)
	} else {
		conflict, err := s.bundlesConflict(verifiedBundles, confCfg)
		if err != nil {
			slog.Warn("retrieval: bundle conflict check failed, falling back to slow path", "error", err)
			return nil, false
		}
		if conflict {
			bundleIDs := make([]string, len(verifiedBundles))
			for i, bm := range verifiedBundles {
				bundleIDs[i] = bm.Bundle.BundleID
			}
			slog.Info("retrieval: multiple verified bundles matched with a contradicts relation between core members, ambiguous, falling back to slow path",
				"link_ids", linkIDs, "bundle_ids", bundleIDs)
			return nil, false
		}
		seen := make(map[string]bool)
		for _, bm := range verifiedBundles {
			for _, pid := range bm.Bundle.CoreMemberPointIDs(confCfg) {
				if seen[pid] {
					continue
				}
				seen[pid] = true
				resolvedPoints = append(resolvedPoints, pid)
			}
		}
	}

	hits, err := s.store.GetCurrentUnitsByPointIDs(resolvedPoints)
	if err != nil {
		slog.Warn("retrieval: fast path bundle unit lookup failed, falling back to slow path", "error", err)
		return nil, false
	}
	if len(hits) == 0 {
		slog.Warn("retrieval: fast path bundle resolved no current KU, falling back to slow path", "point_ids", resolvedPoints)
		return nil, false
	}
	return hits, true
}

// bundlesConflict reports whether any core member of one matched bundle has
// a contradicts KPN relation to any core member of another (docs/impl/v1/
// retrieval.md 步骤 2, reusing the retrieval.Store.GetKPNConflicts primitive
// already used for slow-path KPN expansion).
func (s *Service) bundlesConflict(bundles []activation.BundleMatch, confCfg activation.ConfidenceConfig) (bool, error) {
	conflictSets := make([]map[string]struct{}, len(bundles))
	for i, bm := range bundles {
		neighbors, err := s.store.GetKPNConflicts(bm.Bundle.CoreMemberPointIDs(confCfg))
		if err != nil {
			return false, err
		}
		set := make(map[string]struct{}, len(neighbors))
		for _, n := range neighbors {
			set[n.NeighborPointID] = struct{}{}
		}
		conflictSets[i] = set
	}
	for i := range bundles {
		for j := range bundles {
			if i == j {
				continue
			}
			for _, pid := range bundles[j].Bundle.CoreMemberPointIDs(confCfg) {
				if _, ok := conflictSets[i][pid]; ok {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// sameMemberSet reports order-independent set equality between two
// point_id slices — used to dedupe real-time candidate bundle formation
// against every existing non-deprecated bundle's core members.
func sameMemberSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; !ok {
			return false
		}
	}
	return true
}

// formCandidateBundle implements docs/impl/v1/activation-bundle.md 步骤 4b: a
// real-time formation path, additive to Study's periodic clustering scan
// (internal/study/bundle_scan.go) — a multi-Link hit with no covering
// verified Bundle seeds a candidate bundle from this single observation so
// future identical hits have Bundle material to Match against. Idempotent on
// exact core-member-set identity: a second identical hit appends an observed
// condition to the existing bundle instead of creating a duplicate. Errors
// are logged and swallowed — bundle formation is a side effect, never the
// reason a fast-path turn fails or succeeds.
func (s *Service) formCandidateBundle(workQC QueryContext, pointIDs []string) {
	if s.activationSvc == nil || len(pointIDs) == 0 {
		return
	}
	members := append([]string(nil), pointIDs...)
	sort.Strings(members)

	store := s.activationSvc.Store()
	existing, err := store.ListMatchableBundles()
	if err != nil {
		slog.Warn("retrieval: list matchable bundles for dedup check failed", "error", err)
		return
	}

	questionTerms := text.Terms(text.Normalize(workQC.Question))
	cond := activation.NormalizeObservedCondition(workQC.Subject, workQC.Intent, workQC.Audience, workQC.Constraint, questionTerms, time.Now().UTC())

	for _, b := range existing {
		if sameMemberSet(b.MemberPointIDs(), members) {
			if err := store.AppendBundleObservedCondition(b.BundleID, cond, 50); err != nil {
				slog.Warn("retrieval: append observed condition to existing bundle failed", "bundle_id", b.BundleID, "error", err)
				return
			}
			s.activationSvc.BundleMatcher().InvalidateCache()
			slog.Info("retrieval: appended observed condition to existing candidate bundle from cross-unit ambiguity",
				"bundle_id", b.BundleID, "point_ids", members)
			return
		}
	}

	createdFromJSON, err := json.Marshal([]string{"cross_unit_ambiguity"})
	if err != nil {
		slog.Warn("retrieval: marshal bundle created_from failed", "error", err)
		return
	}
	seedMembers := make([]activation.BundleMember, 0, len(members))
	now := time.Now().UTC()
	for _, pid := range members {
		seedMembers = append(seedMembers, activation.BundleMember{PointID: pid, SuccessCount: 1, FailureCount: 0, LastSeenAt: now})
	}
	newBundle := &activation.ActivationBundle{
		RepresentativeTerms: strings.TrimSpace(workQC.Subject + " " + workQC.Intent),
		ObservedConditions:  []activation.ObservedCondition{cond},
		Members:             seedMembers,
		Status:              activation.BundleStatusCandidate,
		CreatedFrom:         string(createdFromJSON),
	}
	if err := store.CreateBundle(newBundle); err != nil {
		slog.Warn("retrieval: create candidate bundle from cross-unit ambiguity failed", "error", err)
		return
	}
	s.activationSvc.BundleMatcher().InvalidateCache()
	slog.Info("retrieval: created candidate bundle from cross-unit ambiguity",
		"bundle_id", newBundle.BundleID, "point_ids", members)
}

// filterMatchesByDomain keeps links whose KP source is in qc.DomainIDs when
// DomainResolved and DomainIDs non-empty; otherwise returns matches unchanged.
func (s *Service) filterMatchesByDomain(matches []activation.LinkMatch, qc QueryContext) []activation.LinkMatch {
	if !qc.DomainResolved || len(qc.DomainIDs) == 0 || len(matches) == 0 {
		return matches
	}
	pointIDs := make([]string, 0, len(matches))
	for _, m := range matches {
		pointIDs = append(pointIDs, m.Link.PointID)
	}
	domainByPoint, err := s.store.PointDomainIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: point domain lookup failed, skipping domain filter", "error", err)
		return matches
	}
	allowed := make(map[string]struct{}, len(qc.DomainIDs))
	for _, id := range qc.DomainIDs {
		allowed[id] = struct{}{}
	}
	var out []activation.LinkMatch
	for _, m := range matches {
		dom, ok := domainByPoint[m.Link.PointID]
		if !ok {
			continue
		}
		if _, ok := allowed[dom]; ok {
			out = append(out, m)
		}
	}
	if len(out) < len(matches) {
		slog.Info("retrieval: filtered activation matches by domain",
			"before", len(matches), "after", len(out), "domain_ids", qc.DomainIDs)
	}
	return out
}

// session_normalize_tuple 的 Match 前二次规范化调用已废弃（2026-08-12 定案）：
// activation.Match 的第二轮批量模型辅助匹配解决的是同一个问题，不再需要
// 这个独立的调用点。见 docs/impl/v1/retrieval.md 步骤 2、
// docs/impl/v1/plan-parser-vocab-and-unit-ambiguity.md 顶部废弃说明。
