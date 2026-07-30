package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jxman78/wiki-brain/internal/source"
)

// crossBatchMaxSize is the per-batch cap on (new + opposite) knowledge
// points (docs/impl/v1/kpn.md 步骤 2).
const crossBatchMaxSize = 60

// CrossKPNResult is POST /sources/:id/kpn-cross's response
// (docs/impl/v1/kpn.md 步骤 8).
type CrossKPNResult struct {
	SourceID                 string `json:"source_id"`
	Batches                  int    `json:"batches"`
	RelationsCreated         int    `json:"relations_created"`
	ConceptCandidatesTouched int    `json:"concept_candidates_touched"`
}

// CrossSourceKPN implements docs/impl/v1/kpn.md: matches sourceID's KPs
// against same-concept_id KPs from other Sources, creating scope=cross
// related/contradicts relations via the same InsertRelation path the
// intra-Source pass uses (so duplicates are naturally idempotent). KPs
// whose KU has no concept_id do not fall back to a same-domain candidate
// pool (docs/impl/v1/kpn.md 职责 — that produced low-precision relations in
// concept-sparse domains); instead they're handed to the concept evolution
// module (步骤 3) as content_driven concept_candidates, and rejoin cross
// matching once a concept_id is confirmed (步骤 6, RematchPoints). Runs both
// automatically at the end of unit_extract (docs/impl/v1/kpn.md 步骤 1) and
// on-demand via POST /sources/:id/kpn-cross. Failures degrade to partial
// results rather than propagating — the caller (Extract) must not fail the
// Source over this.
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

	groups, orphans := groupPointsForCrossMatch(newPoints, unitConceptMap)

	maxBatches := s.cfg.KPN.CrossMaxBatches
	if maxBatches <= 0 {
		maxBatches = 20
	}

	// Self-ancestor exclusion (docs/impl/v1/wiki.md 步骤 10 "回流的自体循环
	// 必须挡住"; docs/impl/v1/kpn.md 步骤 2 "自体祖先排除"): a wiki_draft
	// reflow source must not build KPN relations against the point_ids its
	// own origin page already cited — those are copies of the same
	// knowledge, not a relation between two pieces of knowledge.
	var ancestorPointIDs map[string]bool
	src, srcErr := s.sourceStore.GetByID(sourceID)
	if srcErr != nil {
		slog.Warn("unit: cross kpn get source failed", "source_id", sourceID, "error", srcErr)
	} else if src.Origin == source.SourceOriginWikiDraft && src.OriginPageID.Valid && src.OriginPageID.String != "" {
		ids, err := s.store.AncestorPointIDsForWikiPage(src.OriginPageID.String)
		if err != nil {
			slog.Warn("unit: cross kpn ancestor lookup failed", "source_id", sourceID, "origin_page_id", src.OriginPageID.String, "error", err)
		} else {
			ancestorPointIDs = make(map[string]bool, len(ids))
			for _, id := range ids {
				ancestorPointIDs[id] = true
			}
		}
	}

	totalSkippedAncestorEdges := 0
	batchesRun := 0
groupLoop:
	for _, g := range groups {
		opposite, err := s.store.GetCrossSourcePointsByConceptID(g.id, sourceID)
		if err != nil {
			slog.Warn("unit: cross kpn get opposite points failed", "concept_id", g.id, "error", err)
			continue
		}
		if len(opposite) == 0 {
			continue
		}

		if len(ancestorPointIDs) > 0 {
			var filtered []KnowledgePoint
			for _, p := range opposite {
				if ancestorPointIDs[p.PointID] {
					totalSkippedAncestorEdges++
					continue
				}
				filtered = append(filtered, p)
			}
			opposite = filtered
			if len(opposite) == 0 {
				continue
			}
		}

		opposite = s.trimOppositeByConfidence(opposite, len(g.points))

		for _, chunk := range splitCrossBatch(g.points, opposite, crossBatchMaxSize) {
			if batchesRun >= maxBatches {
				slog.Warn("unit: cross kpn batch cap reached, dropping remaining groups",
					"source_id", sourceID, "max_batches", maxBatches)
				break groupLoop
			}
			created, _, err := s.crossKPNBatch(ctx, sourceID, chunk.newPoints, chunk.oppositePoints, unitCenterMap)
			if err != nil {
				slog.Warn("unit: cross kpn batch failed", "source_id", sourceID, "error", err)
			}
			result.RelationsCreated += created
			result.Batches++
			batchesRun++
		}
	}

	if totalSkippedAncestorEdges > 0 {
		if err := s.sourceStore.IncrementReflowSkippedEdges(sourceID, totalSkippedAncestorEdges); err != nil {
			slog.Warn("unit: cross kpn record skipped ancestor edges failed", "source_id", sourceID, "error", err)
		}
		slog.Info("unit: cross kpn skipped self-ancestor edges", "source_id", sourceID, "skipped", totalSkippedAncestorEdges)
	}

	if len(orphans) > 0 {
		if srcErr != nil {
			slog.Warn("unit: cross kpn get source for orphan proposal failed", "source_id", sourceID, "error", srcErr)
		} else if src.DomainID.Valid && src.DomainID.String != "" {
			touched, err := s.proposeConceptsForOrphans(ctx, sourceID, src.DomainID.String, orphans, unitCenterMap)
			if err != nil {
				slog.Warn("unit: cross kpn orphan concept proposal failed", "source_id", sourceID, "error", err)
			}
			result.ConceptCandidatesTouched = touched
		} else {
			slog.Debug("unit: cross kpn skip orphan points, source has no domain", "source_id", sourceID, "count", len(orphans))
		}
	}

	slog.Info("unit: cross source kpn done",
		"source_id", sourceID, "batches", result.Batches, "relations_created", result.RelationsCreated,
		"orphan_points", len(orphans), "concept_candidates_touched", result.ConceptCandidatesTouched)
	return result, nil
}

type crossGroup struct {
	id     string // concept_id
	points []KnowledgePoint
}

// groupPointsForCrossMatch implements docs/impl/v1/kpn.md 步骤 2: points
// whose KU has a concept_id are grouped by it for cross-Source matching;
// points with no concept_id are returned separately as orphans rather than
// falling back to a same-domain pool (see CrossSourceKPN's doc comment).
// Order is preserved for deterministic batching.
func groupPointsForCrossMatch(points []KnowledgePoint, unitConceptMap map[string]string) (groups []*crossGroup, orphans []KnowledgePoint) {
	index := make(map[string]*crossGroup)
	var order []string
	for _, p := range points {
		conceptID := unitConceptMap[p.UnitID]
		if conceptID == "" {
			orphans = append(orphans, p)
			continue
		}
		g, ok := index[conceptID]
		if !ok {
			g = &crossGroup{id: conceptID}
			index[conceptID] = g
			order = append(order, conceptID)
		}
		g.points = append(g.points, p)
	}

	groups = make([]*crossGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, index[key])
	}
	return groups, orphans
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
// crossKPNBatch returns the created relation count and the relation_ids that
// were actually inserted (not the IDs of pairs that were no-ops against
// idx_kp_relations_uniq) — RematchPoints needs the latter to let a concept
// candidate restore clean up exactly the relations its own confirm created.
func (s *Service) crossKPNBatch(ctx context.Context, sourceID string, newPoints, opposite []KnowledgePoint, unitCenterMap map[string]string) (int, []string, error) {
	if len(newPoints) == 0 || len(opposite) == 0 {
		return 0, nil, nil
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
		return 0, nil, fmt.Errorf("unit: cross kpn: get opposite units: %w", err)
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

	vars := map[string]string{
		"new_points":      newText.String(),
		"existing_points": oppositeText.String(),
	}

	data, err := s.llmClient.CompleteJSON(ctx, "kpn_cross_match.md", vars, "extraction")
	if err != nil {
		return 0, nil, fmt.Errorf("unit: cross kpn llm call: %w", err)
	}

	var output kpnOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return 0, nil, fmt.Errorf("unit: cross kpn parse: %w", err)
	}

	created, contradicts := 0, 0
	var relationIDs []string
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
			PromptVersion: promptVersionKPNCross,
			Scope:         RelationScopeCross,
		}
		inserted, err := s.store.InsertRelation(r)
		if err != nil {
			slog.Error("unit: insert cross kpn relation failed", "error", err)
			continue
		}
		if inserted {
			created++
			relationIDs = append(relationIDs, r.RelationID)
			if rel.Type == "contradicts" {
				contradicts++
			}
		}
	}

	slog.Info("unit: cross kpn batch done",
		"source_id", sourceID, "new_points", len(newPoints), "opposite_points", len(opposite),
		"relations_created", created, "contradicts", contradicts)
	return created, relationIDs, nil
}

// RematchPoints implements concept.KPNRematchNotifier (docs/impl/v1/kpn.md
// 步骤 6): after a kind=add concept_candidates confirm — new concept or
// "归入已有概念" — gives pointIDs a concept_id, re-run cross-Source matching
// for exactly this batch. These points may have sat with zero cross-Source
// relations the whole time their concept_id was empty (routed to the
// concept evolution module instead of a same-domain fallback). Grouped by
// each point's own source_id so matching never pairs points from the same
// Source against each other. Best-effort: failures are logged, not
// returned — the concept confirm that triggered this already committed.
// Returns the created relation_ids so the caller can record exactly which
// relations this confirm produced (needed to clean them up precisely if the
// candidate is later restored to pending_confirm — restoring must not touch
// relations some unrelated later Source import happened to create once
// these points had a concept_id).
func (s *Service) RematchPoints(conceptID string, pointIDs []string) []string {
	if len(pointIDs) == 0 {
		return nil
	}
	ctx := context.Background()

	points, err := s.store.GetPointsByIDs(pointIDs)
	if err != nil {
		slog.Warn("unit: kpn rematch get points failed", "concept_id", conceptID, "error", err)
		return nil
	}
	if len(points) == 0 {
		return nil
	}

	bySource := make(map[string][]KnowledgePoint)
	var unitIDs []string
	seenUnit := make(map[string]bool, len(points))
	for _, p := range points {
		bySource[p.SourceID] = append(bySource[p.SourceID], p)
		if !seenUnit[p.UnitID] {
			seenUnit[p.UnitID] = true
			unitIDs = append(unitIDs, p.UnitID)
		}
	}
	units, err := s.store.GetUnitsByIDs(unitIDs)
	if err != nil {
		slog.Warn("unit: kpn rematch get units failed", "concept_id", conceptID, "error", err)
		return nil
	}
	unitCenterMap := make(map[string]string, len(units))
	for _, u := range units {
		unitCenterMap[u.UnitID] = u.Center
	}

	totalCreated, totalBatches := 0, 0
	var allRelationIDs []string
	for sourceID, srcPoints := range bySource {
		opposite, err := s.store.GetCrossSourcePointsByConceptID(conceptID, sourceID)
		if err != nil {
			slog.Warn("unit: kpn rematch get opposite points failed", "concept_id", conceptID, "source_id", sourceID, "error", err)
			continue
		}
		if len(opposite) == 0 {
			continue
		}
		opposite = s.trimOppositeByConfidence(opposite, len(srcPoints))
		for _, chunk := range splitCrossBatch(srcPoints, opposite, crossBatchMaxSize) {
			created, relationIDs, err := s.crossKPNBatch(ctx, sourceID, chunk.newPoints, chunk.oppositePoints, unitCenterMap)
			if err != nil {
				slog.Warn("unit: kpn rematch batch failed", "concept_id", conceptID, "source_id", sourceID, "error", err)
				continue
			}
			totalCreated += created
			totalBatches++
			allRelationIDs = append(allRelationIDs, relationIDs...)
		}
	}

	slog.Info("unit: kpn rematch after concept confirm done",
		"concept_id", conceptID, "point_count", len(points), "batches", totalBatches, "relations_created", totalCreated)
	return allRelationIDs
}
