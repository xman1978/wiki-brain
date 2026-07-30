// Page relation derivation (docs/impl/v1/wiki.md 步骤 7): related/contradicts
// rows between published concept pages, derived purely from
// knowledge_point_relations + shared source_point_ids — no LLM call.
package wiki

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// RecomputeRelationsForPage implements docs/impl/v1/wiki.md 步骤 7a: on
// publish of a concept page, fully rewrite its related/contradicts rows
// against every other published concept page. Only ever touches rows
// involving pageID — other pages' relations to each other are untouched.
func (s *Service) RecomputeRelationsForPage(pageID string) error {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return fmt.Errorf("wiki: recompute relations: get page: %w", err)
	}
	if page == nil || page.Status != StatusPublished || page.PageType != PageTypeConcept {
		return nil
	}

	others, err := s.store.ListPublishedConceptPagesWithPoints()
	if err != nil {
		return fmt.Errorf("wiki: recompute relations: list published concept pages: %w", err)
	}

	var aIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &aIDs)
	aCurrent, err := s.store.CurrentPointIDs(aIDs)
	if err != nil {
		return fmt.Errorf("wiki: recompute relations: filter lifecycle: %w", err)
	}

	if err := s.store.DeletePageRelationsInvolving(pageID, RelationRelated, RelationContradicts); err != nil {
		return fmt.Errorf("wiki: recompute relations: delete existing: %w", err)
	}

	relationKPNMin := s.cfg.RelationKPNMin
	if relationKPNMin <= 0 {
		relationKPNMin = 1
	}
	sharedMin := s.cfg.RelationSharedPointMin
	if sharedMin <= 0 {
		sharedMin = 2
	}

	for _, other := range others {
		if other.PageID == pageID {
			continue
		}
		bCurrent, err := s.store.CurrentPointIDs(other.PointIDs)
		if err != nil {
			slog.Warn("wiki: recompute relations: filter lifecycle for peer failed", "page_id", other.PageID, "error", err)
			continue
		}
		if len(aCurrent) == 0 || len(bCurrent) == 0 {
			continue
		}

		relatedCount, contradictsCount, shared, err := s.store.CountPointRelationsBetween(aCurrent, bCurrent)
		if err != nil {
			slog.Warn("wiki: recompute relations: count between failed", "page_a", pageID, "page_b", other.PageID, "error", err)
			continue
		}

		isRelated := relatedCount >= relationKPNMin || len(shared) >= sharedMin
		isContradicts := contradictsCount >= 1

		if isRelated {
			ev, _ := json.Marshal(RelationEvidence{SharedPointIDs: shared, KPNRelationCount: relatedCount})
			if err := s.store.UpsertPageRelation(pageID, other.PageID, RelationRelated, DerivedFromKPN, string(ev)); err != nil {
				slog.Warn("wiki: upsert related relation failed", "page_a", pageID, "page_b", other.PageID, "error", err)
			}
		}
		if isContradicts {
			ev, _ := json.Marshal(RelationEvidence{SharedPointIDs: shared, KPNRelationCount: contradictsCount})
			if err := s.store.UpsertPageRelation(pageID, other.PageID, RelationContradicts, DerivedFromKPN, string(ev)); err != nil {
				slog.Warn("wiki: upsert contradicts relation failed", "page_a", pageID, "page_b", other.PageID, "error", err)
			}
		}
	}
	return nil
}

// RecomputeRelationsForPoints implements docs/impl/v1/wiki.md 步骤 7b: after
// new cross-Source KPN relations appear, recompute only the page pairs whose
// published concept pages cite one of the affected point_ids — not a full
// pairwise rescan.
func (s *Service) RecomputeRelationsForPoints(pointIDs []string) error {
	if len(pointIDs) == 0 {
		return nil
	}
	affected := make(map[string]bool)
	pages, err := s.store.ListPublishedConceptPagesWithPoints()
	if err != nil {
		return fmt.Errorf("wiki: recompute relations for points: list pages: %w", err)
	}
	changed := make(map[string]bool, len(pointIDs))
	for _, id := range pointIDs {
		changed[id] = true
	}
	for _, p := range pages {
		for _, pid := range p.PointIDs {
			if changed[pid] {
				affected[p.PageID] = true
				break
			}
		}
	}
	for pageID := range affected {
		if err := s.RecomputeRelationsForPage(pageID); err != nil {
			slog.Error("wiki: recompute relations for point-affected page failed", "page_id", pageID, "error", err)
		}
	}
	return nil
}

// clearRelationsForPage implements docs/impl/v1/wiki.md 步骤 7 "清理": on
// archive, delete every related/contradicts row involving the page. contains
// rows are handled separately (docs/impl/v1/wiki.md 步骤 9).
func (s *Service) clearRelationsForPage(pageID string) {
	if err := s.store.DeletePageRelationsInvolving(pageID, RelationRelated, RelationContradicts); err != nil {
		slog.Warn("wiki: clear relations on archive failed", "page_id", pageID, "error", err)
	}
}
