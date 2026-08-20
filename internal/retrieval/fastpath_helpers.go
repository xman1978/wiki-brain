package retrieval

import (
	"context"
	"log/slog"

	"github.com/jxman78/wiki-brain/internal/activation"
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

// bundleCandidate is the resolved result of a Bundle Match this round —
// mirrors what resolveUnitsForPoints produces for Link, plus the tier/mean
// needed to compare against a Link candidate on the same axis (docs/impl/v1/
// retrieval.md 步骤 2「命中优先级」).
type bundleCandidate struct {
	hits      []DirectHit
	bundleIDs []string
	tier      activation.Tier
	mean      float64
	// hitInfo carries per-bundle Trace-ready detail (2026-08-20 阶段 2「验证」
	// 接线) — one BundleHit per bundle actually merged into bundleIDs, each
	// with its own matched condition's quadruple/tier and the member
	// point_ids that specific bundle contributed to resolvedPoints, so
	// trace.recordBundleHitOutcome can call RecordBundleOutcome/
	// RecordMemberOutcome against the exact condition/members this round
	// used, without re-deriving them from the merged, bundle-agnostic hits.
	hitInfo []BundleHit
}

// resolveBundleCandidate implements docs/impl/v1/retrieval.md 步骤 2's Bundle
// matching (2026-08-20 重设计，取代此前"仅在 Link 跨 unit 歧义时才 consult，
// 未覆盖则实时新建候选"的口径): runs independently of Link matching, in
// parallel with it (see tryFastPath) — Bundle no longer needs Link's
// ambiguity as a trigger, and no longer seeds a candidate Bundle from an
// unresolved hit here (that was direct evidence conflation with "两条 Link
// 各自独立命中同一问题" — a weaker signal that should only trigger slow-path
// re-verification, not a Bundle merge; see docs/design/activation-bundle.md
// 改判). A miss here is simply "no Bundle candidate this round" — the caller
// falls back to whatever the Link side resolved, or the slow path.
func (s *Service) resolveBundleCandidate(ctx context.Context, expandedQuery session.ExpandedQuery, matchCfg activation.MatchConfig) (bundleCandidate, bool) {
	matches, err := s.activationSvc.MatchBundles(ctx, expandedQuery, matchCfg)
	if err != nil {
		slog.Warn("retrieval: bundle match failed", "error", err)
		return bundleCandidate{}, false
	}
	if len(matches) == 0 {
		return bundleCandidate{}, false
	}

	// 2026-08-13: 核心成员不再是建/刷新 Bundle 那一刻写死的静态数组，是
	// 每个成员自己的 mean(member) 越过 serving_confidence_min 派生出来的
	// 结果（docs/impl/v1/activation-bundle.md「成员置信度」组装时的用法）。
	confCfg := s.activationSvc.ConfidenceConfig()
	var resolvedPoints []string
	bundleIDs := make([]string, 0, len(matches))
	if len(matches) == 1 {
		resolvedPoints = matches[0].Bundle.CoreMemberPointIDs(confCfg)
		bundleIDs = append(bundleIDs, matches[0].Bundle.BundleID)
	} else {
		conflict, err := s.bundlesConflict(matches, confCfg)
		if err != nil {
			slog.Warn("retrieval: bundle conflict check failed", "error", err)
			return bundleCandidate{}, false
		}
		if conflict {
			ids := make([]string, len(matches))
			for i, bm := range matches {
				ids[i] = bm.Bundle.BundleID
			}
			slog.Info("retrieval: multiple bundles matched with a contradicts relation between core members, ambiguous, ignoring bundle candidate",
				"bundle_ids", ids)
			return bundleCandidate{}, false
		}
		seen := make(map[string]bool)
		for _, bm := range matches {
			bundleIDs = append(bundleIDs, bm.Bundle.BundleID)
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
		slog.Warn("retrieval: fast path bundle unit lookup failed", "error", err)
		return bundleCandidate{}, false
	}
	if len(hits) == 0 {
		slog.Warn("retrieval: fast path bundle resolved no current KU", "point_ids", resolvedPoints)
		return bundleCandidate{}, false
	}

	tier, mean := bestBundleTierMean(matches)
	hitInfo := make([]BundleHit, len(matches))
	for i, bm := range matches {
		hitInfo[i] = BundleHit{
			BundleID: bm.Bundle.BundleID, MatchScore: bm.Score, MatchedBy: bm.MatchedBy,
			Tier: string(bm.Tier), AuditSampled: bm.AuditSampled,
			Subject: bm.Subject, Intent: bm.Intent, Audience: bm.Audience, Constraint: bm.Constraint,
			MemberPointIDs: bm.Bundle.CoreMemberPointIDs(confCfg),
		}
	}
	return bundleCandidate{hits: hits, bundleIDs: bundleIDs, tier: tier, mean: mean, hitInfo: hitInfo}, true
}

// bestBundleTierMean / bestLinkTierMean / tierRank implement the "same axis"
// comparison docs/impl/v1/retrieval.md 步骤 2 uses to pick between a resolved
// Link candidate and a resolved Bundle candidate: trusted > self_graded >
// exploring, tie-broken by mean.
func bestBundleTierMean(matches []activation.BundleMatch) (activation.Tier, float64) {
	best := matches[0]
	for _, m := range matches[1:] {
		if tierRank(m.Tier) > tierRank(best.Tier) || (tierRank(m.Tier) == tierRank(best.Tier) && m.Mean > best.Mean) {
			best = m
		}
	}
	return best.Tier, best.Mean
}

func bestLinkTierMean(matches []activation.LinkMatch) (activation.Tier, float64) {
	best := matches[0]
	for _, m := range matches[1:] {
		if tierRank(m.Tier) > tierRank(best.Tier) || (tierRank(m.Tier) == tierRank(best.Tier) && m.Mean > best.Mean) {
			best = m
		}
	}
	return best.Tier, best.Mean
}

func tierRank(t activation.Tier) int {
	switch t {
	case activation.TierTrusted:
		return 2
	case activation.TierSelfGraded:
		return 1
	default:
		return 0
	}
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
