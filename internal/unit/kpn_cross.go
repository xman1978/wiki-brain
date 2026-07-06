package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// crossBatchMaxSize is the per-batch cap on (new + opposite) knowledge
// points (docs/impl/v1/kpn.md 步骤 2).
const crossBatchMaxSize = 60

// CrossKPNResult is POST /sources/:id/kpn-cross's response
// (docs/impl/v1/kpn.md 步骤 7).
type CrossKPNResult struct {
	SourceID         string `json:"source_id"`
	Batches          int    `json:"batches"`
	RelationsCreated int    `json:"relations_created"`
}

// CrossSourceKPN implements docs/impl/v1/kpn.md: matches sourceID's KPs
// against KPs from other Sources (same concept_id, or same domain_id when
// the KU has no concept), creating scope=cross related/contradicts
// relations via the same InsertRelation path the intra-Source pass uses (so
// duplicates are naturally idempotent). Runs both automatically at the end
// of unit_extract (docs/impl/v1/kpn.md 步骤 1) and on-demand via
// POST /sources/:id/kpn-cross. Failures degrade to partial results rather
// than propagating — the caller (Extract) must not fail the Source over this.
func (s *Service) CrossSourceKPN(ctx context.Context, sourceID string) (*CrossKPNResult, error) {
	result := &CrossKPNResult{SourceID: sourceID}

	newPoints, err := s.store.GetPointsBySourceID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: cross kpn: get points: %w", err)
	}
	if len(newPoints) == 0 {
		return result, nil
	}

	units, err := s.store.GetCompletedUnitsBySourceID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: cross kpn: get units: %w", err)
	}
	unitConceptMap := make(map[string]string, len(units))
	unitCenterMap := make(map[string]string, len(units))
	for _, u := range units {
		if u.ConceptID.Valid {
			unitConceptMap[u.UnitID] = u.ConceptID.String
		}
		unitCenterMap[u.UnitID] = u.Center
	}

	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: cross kpn: get source: %w", err)
	}
	sourceDomainID := ""
	if src.DomainID.Valid {
		sourceDomainID = src.DomainID.String
	}

	groups := groupPointsForCrossMatch(newPoints, unitConceptMap, sourceDomainID)

	maxBatches := s.cfg.KPN.CrossMaxBatches
	if maxBatches <= 0 {
		maxBatches = 20
	}

	batchesRun := 0
groupLoop:
	for _, g := range groups {
		var opposite []KnowledgePoint
		if g.kind == "concept" {
			opposite, err = s.store.GetCrossSourcePointsByConceptID(g.id, sourceID)
		} else {
			opposite, err = s.store.GetCrossSourcePointsByDomainID(g.id, sourceID)
		}
		if err != nil {
			slog.Warn("unit: cross kpn get opposite points failed", "kind", g.kind, "id", g.id, "error", err)
			continue
		}
		if len(opposite) == 0 {
			continue
		}

		opposite = s.trimOppositeByConfidence(opposite, len(g.points))

		for _, chunk := range splitCrossBatch(g.points, opposite, crossBatchMaxSize) {
			if batchesRun >= maxBatches {
				slog.Warn("unit: cross kpn batch cap reached, dropping remaining groups",
					"source_id", sourceID, "max_batches", maxBatches)
				break groupLoop
			}
			created, err := s.crossKPNBatch(ctx, sourceID, chunk.newPoints, chunk.oppositePoints, unitCenterMap)
			if err != nil {
				slog.Warn("unit: cross kpn batch failed", "source_id", sourceID, "error", err)
			}
			result.RelationsCreated += created
			result.Batches++
			batchesRun++
		}
	}

	slog.Info("unit: cross source kpn done",
		"source_id", sourceID, "batches", result.Batches, "relations_created", result.RelationsCreated)
	return result, nil
}

type crossGroup struct {
	kind   string // "concept" / "domain"
	id     string
	points []KnowledgePoint
}

// groupPointsForCrossMatch implements docs/impl/v1/kpn.md 步骤 2's 对端 KP
// 集合 priority: group by the KU's concept_id when present, else by the
// Source's domain_id, else the point is skipped entirely (no way to find a
// meaningful opposite set). Order is preserved for deterministic batching.
func groupPointsForCrossMatch(points []KnowledgePoint, unitConceptMap map[string]string, sourceDomainID string) []*crossGroup {
	index := make(map[string]*crossGroup)
	var order []string
	for _, p := range points {
		conceptID := unitConceptMap[p.UnitID]
		var kind, id string
		switch {
		case conceptID != "":
			kind, id = "concept", conceptID
		case sourceDomainID != "":
			kind, id = "domain", sourceDomainID
		default:
			slog.Debug("unit: cross kpn skip point, no concept or domain", "point_id", p.PointID)
			continue
		}
		key := kind + ":" + id
		g, ok := index[key]
		if !ok {
			g = &crossGroup{kind: kind, id: id}
			index[key] = g
			order = append(order, key)
		}
		g.points = append(g.points, p)
	}

	groups := make([]*crossGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, index[key])
	}
	return groups
}

// trimOppositeByConfidence keeps a group's total within crossBatchMaxSize by
// dropping the lowest question_kp_cooccurrence.confident_count opposite
// candidates first (docs/impl/v1/kpn.md 步骤 2, "优先与被验证过的知识建立关系").
func (s *Service) trimOppositeByConfidence(opposite []KnowledgePoint, newCount int) []KnowledgePoint {
	budget := crossBatchMaxSize - newCount
	if budget < 0 {
		budget = 0
	}
	if len(opposite) <= budget {
		return opposite
	}

	pointIDs := make([]string, len(opposite))
	for i, p := range opposite {
		pointIDs[i] = p.PointID
	}
	counts, err := s.store.ConfidentCountByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("unit: cross kpn confident count lookup failed, truncating without prioritization", "error", err)
		counts = make(map[string]int)
	}

	sorted := make([]KnowledgePoint, len(opposite))
	copy(sorted, opposite)
	sort.SliceStable(sorted, func(i, j int) bool {
		return counts[sorted[i].PointID] > counts[sorted[j].PointID]
	})
	return sorted[:budget]
}

type crossBatchChunk struct {
	newPoints      []KnowledgePoint
	oppositePoints []KnowledgePoint
}

// splitCrossBatch hard-splits (new, opposite) into ≤maxSize-total chunks —
// reached when a group's new-point count alone already exceeds maxSize, so
// confidence-based trimming of the opposite side (which runs first) wasn't
// enough (docs/impl/v1/kpn.md 步骤 2, "仍超则硬切多批").
func splitCrossBatch(newPoints, opposite []KnowledgePoint, maxSize int) []crossBatchChunk {
	if len(newPoints)+len(opposite) <= maxSize {
		return []crossBatchChunk{{newPoints: newPoints, oppositePoints: opposite}}
	}

	var chunks []crossBatchChunk
	oppRemaining := opposite
	for i := 0; i < len(newPoints); i += maxSize {
		end := i + maxSize
		if end > len(newPoints) {
			end = len(newPoints)
		}
		newChunk := newPoints[i:end]

		budget := maxSize - len(newChunk)
		if budget < 0 {
			budget = 0
		}
		oppChunk := oppRemaining
		if len(oppChunk) > budget {
			oppChunk = oppChunk[:budget]
		}
		oppRemaining = oppRemaining[len(oppChunk):]

		chunks = append(chunks, crossBatchChunk{newPoints: newChunk, oppositePoints: oppChunk})
	}
	return chunks
}

// crossKPNBatch runs one LLM call for a (new, opposite) pairing and writes
// the resulting relations (docs/impl/v1/kpn.md 步骤 3-4). Returns the number
// of relations actually inserted (excludes duplicates INSERT OR IGNORE skipped).
func (s *Service) crossKPNBatch(ctx context.Context, sourceID string, newPoints, opposite []KnowledgePoint, unitCenterMap map[string]string) (int, error) {
	if len(newPoints) == 0 || len(opposite) == 0 {
		return 0, nil
	}

	oppositeUnitIDs := make([]string, 0, len(opposite))
	seenUnit := make(map[string]bool, len(opposite))
	for _, p := range opposite {
		if !seenUnit[p.UnitID] {
			seenUnit[p.UnitID] = true
			oppositeUnitIDs = append(oppositeUnitIDs, p.UnitID)
		}
	}
	oppositeUnits, err := s.store.GetUnitsByIDs(oppositeUnitIDs)
	if err != nil {
		return 0, fmt.Errorf("unit: cross kpn: get opposite units: %w", err)
	}
	oppositeCenterMap := make(map[string]string, len(oppositeUnits))
	for _, u := range oppositeUnits {
		oppositeCenterMap[u.UnitID] = u.Center
	}

	newPointIDs := make(map[string]bool, len(newPoints))
	var newText strings.Builder
	for _, p := range newPoints {
		newPointIDs[p.PointID] = true
		fmt.Fprintf(&newText, "%s\t%s\t%s\n", p.PointID, unitCenterMap[p.UnitID], p.Content)
	}

	oppositePointIDs := make(map[string]bool, len(opposite))
	var oppositeText strings.Builder
	for _, p := range opposite {
		oppositePointIDs[p.PointID] = true
		fmt.Fprintf(&oppositeText, "%s\t%s\t%s\n", p.PointID, oppositeCenterMap[p.UnitID], p.Content)
	}

	schemaJSON := `{"relations":[{"from":"point_id","to":"point_id","type":"related|contradicts"}]}`
	vars := map[string]string{
		"new_points":      newText.String(),
		"existing_points": oppositeText.String(),
		"json_schema":     schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "kpn_cross_match.md", vars, "extraction")
	if err != nil {
		return 0, fmt.Errorf("unit: cross kpn llm call: %w", err)
	}

	var output kpnOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return 0, fmt.Errorf("unit: cross kpn parse: %w", err)
	}

	created, contradicts := 0, 0
	for _, rel := range output.Relations {
		// direction constraint: cross-Source relations always run new→existing.
		if !newPointIDs[rel.From] || !oppositePointIDs[rel.To] {
			continue
		}
		if rel.From == rel.To {
			continue
		}
		if rel.Type != "related" && rel.Type != "contradicts" {
			continue
		}

		r := &KnowledgePointRelation{
			SourcePointID: rel.From,
			TargetPointID: rel.To,
			RelationType:  rel.Type,
			Direction:     "bidirectional",
			PromptVersion: "v1",
			Scope:         RelationScopeCross,
		}
		inserted, err := s.store.InsertRelation(r)
		if err != nil {
			slog.Error("unit: insert cross kpn relation failed", "error", err)
			continue
		}
		if inserted {
			created++
			if rel.Type == "contradicts" {
				contradicts++
			}
		}
	}

	slog.Info("unit: cross kpn batch done",
		"source_id", sourceID, "new_points", len(newPoints), "opposite_points", len(opposite),
		"relations_created", created, "contradicts", contradicts)
	return created, nil
}
