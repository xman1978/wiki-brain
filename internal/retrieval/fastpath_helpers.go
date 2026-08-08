package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

func classifyActivationMatches(matches []activation.LinkMatch) ([]ActivationHit, []activation.LinkMatch, bool) {
	activationHits := make([]ActivationHit, len(matches))
	allLinkIDs := make([]string, len(matches))
	for i, m := range matches {
		activationHits[i] = ActivationHit{LinkID: m.Link.LinkID, PointID: m.Link.PointID, MatchScore: m.Score}
		allLinkIDs[i] = m.Link.LinkID
	}
	slog.Info("retrieval: activation layer matched", "link_count", len(matches), "link_ids", allLinkIDs)

	var verified []activation.LinkMatch
	for _, m := range matches {
		if m.Link.Status == activation.StatusVerified {
			verified = append(verified, m)
		}
	}
	if len(verified) == 0 {
		slog.Info("retrieval: activation matched candidate-only, recording hits and falling back to slow path",
			"link_ids", allLinkIDs)
		return activationHits, nil, false
	}
	return activationHits, verified, true
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

func (s *Service) resolveSingleUnitHits(pointIDs, linkIDs []string, _ []ActivationHit) ([]DirectHit, bool) {
	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: fast path unit lookup failed, falling back to slow path", "error", err)
		return nil, false
	}
	if len(hits) == 0 {
		slog.Warn("retrieval: fast path found no current KU for matched links, falling back", "link_ids", linkIDs)
		return nil, false
	}
	unitSet := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		unitSet[h.UnitID] = struct{}{}
	}
	if len(unitSet) > 1 {
		unitIDs := make([]string, 0, len(unitSet))
		for uid := range unitSet {
			unitIDs = append(unitIDs, uid)
		}
		slog.Info("retrieval: activation matched verified links across multiple units, ambiguous, falling back to slow path",
			"link_ids", linkIDs, "unit_ids", unitIDs)
		return nil, false
	}
	return hits, true
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

type normalizedTuple struct {
	Subject    string
	Intent     string
	Audience   string
	Constraint string
}

func (s *Service) normalizeTuple(ctx context.Context, qc QueryContext, vocab string) (*normalizedTuple, error) {
	resp, err := s.llmClient.CompleteJSON(ctx, "session_normalize_tuple.md", map[string]string{
		"question":               qc.Question,
		"subject":                qc.Subject,
		"intent":                 qc.Intent,
		"audience":               qc.Audience,
		"constraint":             qc.Constraint,
		"known_condition_groups": vocab,
	}, "classification")
	if err != nil {
		return nil, err
	}
	var out struct {
		Subject    string `json:"subject"`
		Intent     string `json:"intent"`
		Audience   string `json:"audience"`
		Constraint string `json:"constraint"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("parse normalize response: %w", err)
	}
	if strings.TrimSpace(out.Intent) == "" && strings.TrimSpace(out.Subject) == "" {
		return nil, fmt.Errorf("normalize returned empty subject and intent")
	}
	return &normalizedTuple{
		Subject:    out.Subject,
		Intent:     out.Intent,
		Audience:   out.Audience,
		Constraint: out.Constraint,
	}, nil
}

// acceptNormalizedTuple reports whether norm aligns to at least one observed
// condition group (same MatchConditionGroups semantics) and does not flip a
// non-empty original constraint to a conflicting one (product gating).
func acceptNormalizedTuple(orig QueryContext, norm *normalizedTuple, groups []activation.ObservedCondition, resolver *activation.SynonymResolver) bool {
	if norm == nil || len(groups) == 0 {
		return false
	}
	if resolver == nil {
		resolver = activation.NewSynonymResolver()
	}
	origQC := text.Terms(text.Normalize(orig.Constraint))
	newQC := text.Terms(text.Normalize(norm.Constraint))
	if origQC != "" && newQC != "" && origQC != newQC {
		return false
	}
	queryTopic, qi, qa, qc := activation.BuildQueryConditionTerms(
		norm.Subject, norm.Intent, norm.Audience, norm.Constraint, resolver,
	)
	return activation.MatchConditionGroups(groups, queryTopic, qi, qa, qc, resolver)
}

// maybeNormalizeQueryBeforeMatch runs session_normalize_tuple against domain
// verified condition groups and returns an updated QueryContext when the
// result is accepted; otherwise returns qc unchanged.
func (s *Service) maybeNormalizeQueryBeforeMatch(ctx context.Context, qc QueryContext) QueryContext {
	if s.activationSvc == nil || !qc.DomainResolved || len(qc.DomainIDs) == 0 {
		return qc
	}
	groups, err := s.activationSvc.Store().ListVerifiedConditionGroups(qc.DomainIDs, 40)
	if err != nil {
		slog.Warn("retrieval: load condition groups for normalize failed", "error", err)
		return qc
	}
	if len(groups) == 0 {
		return qc
	}
	var vocab strings.Builder
	for _, c := range groups {
		fmt.Fprintf(&vocab, "- subject=%s | intent=%s | audience=%s | constraint=%s\n",
			c.Subject, c.Intent, c.Audience, c.Constraint)
	}
	norm, nerr := s.normalizeTuple(ctx, qc, vocab.String())
	if nerr != nil {
		slog.Warn("retrieval: tuple normalize failed, keeping parse tuple", "error", nerr)
		return qc
	}
	resolver := activation.NewSynonymResolver()
	if syns, serr := s.activationSvc.Store().ListActiveSynonyms(); serr == nil {
		resolver.Load(syns)
	}
	if !acceptNormalizedTuple(qc, norm, groups, resolver) {
		slog.Info("retrieval: normalized tuple rejected (no group align or constraint conflict), keeping parse tuple",
			"orig_subject", qc.Subject, "norm_subject", norm.Subject)
		return qc
	}
	slog.Info("retrieval: tuple normalized before Match",
		"subject", norm.Subject, "intent", norm.Intent, "constraint", norm.Constraint)
	out := qc
	out.Subject = norm.Subject
	out.Intent = norm.Intent
	out.Audience = norm.Audience
	out.Constraint = norm.Constraint
	return out
}
