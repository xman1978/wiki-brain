package mcp

import "github.com/jxman78/wiki-brain/internal/unit"

// wikiCitationsFromPoints resolves a Wiki direct-answer's CitedPointIDs into
// the same {content, citation} shape as the normal DirectEvidence/Supporting
// path. The Wiki path only carries point_id references (no per-item
// Content/SourceRef, see EvidenceSet.WikiAnswerContent), so KP content and
// its parent unit's line range are looked up here instead — see
// docs/impl/v1/mcp.md 步骤 3.
func (s *Server) wikiCitationsFromPoints(resolver *citationResolver, pointIDs []string) ([]EvidenceItem, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}

	points, err := s.unitStore.GetPointsByIDs(pointIDs)
	if err != nil {
		return nil, err
	}

	unitIDs := make([]string, 0, len(points))
	for _, p := range points {
		unitIDs = append(unitIDs, p.UnitID)
	}
	units, err := s.unitStore.GetUnitsByIDs(unitIDs)
	if err != nil {
		return nil, err
	}
	unitByID := make(map[string]unit.KnowledgeUnit, len(units))
	for _, u := range units {
		unitByID[u.UnitID] = u
	}

	out := make([]EvidenceItem, 0, len(points))
	for _, p := range points {
		var citation Citation
		if u, ok := unitByID[p.UnitID]; ok {
			citation = resolver.resolve(u.SourceID, u.LineStart)
		}
		out = append(out, EvidenceItem{
			Content:  p.Content,
			Citation: citation,
			Role:     "direct",
		})
	}
	return out, nil
}
