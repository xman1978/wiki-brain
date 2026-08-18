// Knowledge subgraph expansion for single-tier compile
// (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3,
// docs/design/wiki-single-tier-revision.md "材料选取：Core / Context /
// Conflict 子图"). Core/Context/Conflict replaces the old aspect-clustering
// material grouping as the top-level structure fed into the analyze/compile
// prompts.
package wiki

import (
	"fmt"
	"log/slog"
)

// buildKnowledgeSubgraph implements the design doc's Core/Context/Conflict
// rule for a human-picked entry_id set:
//
//	Core(entry)    = entry 直接归属的 KP（lifecycle=current）；
//	                 entry.kind=fact 且 parent_entry_id 非空时，父 Concept 的
//	                 Core 一并纳入，标注为「背景」；
//	Context(entry) = Core 中 KP 的一跳 related；
//	Conflict(entry)= Core 中 KP 的一跳 contradicts；
//
// related/contradicts only expand one hop, never recursively — Context/
// Conflict points are never themselves expanded again. Core has no size cap
// (docs/design/wiki-single-tier-revision.md「已拍板」第 3 条: a parent
// Concept's own KP count is already bounded by entry-assignment judgment,
// not an unbounded graph walk).
func (s *Service) buildKnowledgeSubgraph(entryIDs []string) (core, context, conflict []QualifyingPoint, err error) {
	if len(entryIDs) == 0 {
		return nil, nil, nil, fmt.Errorf("wiki: buildKnowledgeSubgraph: entryIDs is empty")
	}

	coreByID := make(map[string]QualifyingPoint)
	parentsSeen := make(map[string]bool)

	addCore := func(points []QualifyingPoint, role string) {
		for _, p := range points {
			existing, ok := coreByID[p.PointID]
			// A point already tagged Core (own entry) keeps that role even if
			// it's also reachable as another entry's parent-background —
			// "本页核心" outranks "背景" when both apply.
			if ok && existing.SubgraphRole == SubgraphRoleCore {
				continue
			}
			p.SubgraphRole = role
			coreByID[p.PointID] = p
		}
	}

	for _, entryID := range entryIDs {
		own, err := s.store.ListQualifyingPoints(entryID, false)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("wiki: subgraph core for entry %s: %w", entryID, err)
		}
		addCore(own, SubgraphRoleCore)

		_, _, _, kind, parentEntryID, err := s.store.GetEntryInfo(entryID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("wiki: subgraph entry info for %s: %w", entryID, err)
		}
		if kind == "fact" && parentEntryID != "" && !parentsSeen[parentEntryID] {
			parentsSeen[parentEntryID] = true
			parentPoints, err := s.store.ListQualifyingPoints(parentEntryID, false)
			if err != nil {
				slog.Warn("wiki: subgraph parent concept core lookup failed, continuing without it",
					"entry_id", entryID, "parent_entry_id", parentEntryID, "error", err)
				continue
			}
			addCore(parentPoints, SubgraphRoleCoreParentBackground)
		}
	}

	if len(coreByID) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: entries %v currently have no current knowledge points", ErrNoQualifyingPoints, entryIDs)
	}

	coreIDs := make([]string, 0, len(coreByID))
	for id := range coreByID {
		coreIDs = append(coreIDs, id)
		core = append(core, coreByID[id])
	}

	context, err = s.expandOneHop(coreIDs, coreByID, "related")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wiki: subgraph context expansion: %w", err)
	}
	conflict, err = s.expandOneHop(coreIDs, coreByID, "contradicts")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wiki: subgraph conflict expansion: %w", err)
	}
	return core, context, conflict, nil
}

// expandOneHop resolves relationType-typed KPN edges touching any of
// coreIDs, then fetches content for whichever endpoint isn't already in
// Core — this is deliberately "one hop out of Core", not "one hop from every
// point already gathered" (no recursion, docs/impl/v1/
// wiki-single-tier-task-brief.md 步骤 3 第 5 条).
func (s *Service) expandOneHop(coreIDs []string, coreByID map[string]QualifyingPoint, relationType string) ([]QualifyingPoint, error) {
	rels, err := s.store.RelationsFromPoints(coreIDs, relationType)
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, nil
	}

	coreSet := make(map[string]bool, len(coreByID))
	for id := range coreByID {
		coreSet[id] = true
	}
	seen := make(map[string]bool)
	var otherIDs []string
	for _, r := range rels {
		for _, id := range []string{r.SourcePointID, r.TargetPointID} {
			if coreSet[id] || seen[id] {
				continue
			}
			seen[id] = true
			otherIDs = append(otherIDs, id)
		}
	}
	if len(otherIDs) == 0 {
		return nil, nil
	}
	return s.store.PointsByIDs(otherIDs)
}
