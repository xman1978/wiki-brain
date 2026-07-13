package unit

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ApplyOfflineMerge collapses a human-confirmed duplicate cluster in already-
// stored data (存量治理, driven by cmd/dedup-report -apply). survivorID keeps
// its own center; its line range widens to the union of the whole cluster.
// Each merged unit's points are REPARENTED to the survivor when their
// normalized content isn't already among the survivor's points — moved, not
// copied, because traces/activation links/KPN relations reference point_id
// and must keep resolving. Redundant points stay behind on their unit, and
// the merged units are then marked superseded via SetUnitLifecycle (the
// reason records the survivor), cascading lifecycle to those leftover points
// and cleaning the bleve indexes — nothing is hard-deleted.
//
// This is deliberately deterministic (no LLM): the human already confirmed
// "duplicate", and a union-with-dedup merge cannot lose information, unlike
// an LLM rewrite. Extraction-time merges keep using judgePair.
func (s *Service) ApplyOfflineMerge(survivorID string, mergedIDs []string, reason string) error {
	survivor, err := s.store.GetUnitByID(survivorID)
	if err != nil {
		return fmt.Errorf("unit: apply merge: load survivor: %w", err)
	}
	if survivor.Lifecycle != LifecycleCurrent {
		return fmt.Errorf("unit: apply merge: survivor %s lifecycle is %q, want current", survivorID, survivor.Lifecycle)
	}

	survivorPoints, err := s.store.GetPointsByUnitID(survivorID)
	if err != nil {
		return fmt.Errorf("unit: apply merge: load survivor points: %w", err)
	}
	seen := make(map[string]bool, len(survivorPoints))
	for _, p := range survivorPoints {
		seen[normText(p.Content)] = true
	}

	newStart, newEnd := survivor.LineStart, survivor.LineEnd
	moved := 0
	for _, id := range mergedIDs {
		mu, err := s.store.GetUnitByID(id)
		if err != nil {
			return fmt.Errorf("unit: apply merge: load merged unit %s: %w", id, err)
		}
		if mu.SourceID != survivor.SourceID {
			return fmt.Errorf("unit: apply merge: %s belongs to source %s, survivor to %s — refusing cross-source merge", id, mu.SourceID, survivor.SourceID)
		}
		if mu.Lifecycle != LifecycleCurrent {
			return fmt.Errorf("unit: apply merge: %s lifecycle is %q, want current", id, mu.Lifecycle)
		}
		if mu.LineStart < newStart {
			newStart = mu.LineStart
		}
		if mu.LineEnd > newEnd {
			newEnd = mu.LineEnd
		}

		points, err := s.store.GetPointsByUnitID(id)
		if err != nil {
			return fmt.Errorf("unit: apply merge: load points of %s: %w", id, err)
		}
		for _, p := range points {
			key := normText(p.Content)
			if key == "" || seen[key] {
				continue // redundant — stays behind, superseded with its unit
			}
			if err := s.store.MovePointToUnit(p.PointID, survivorID); err != nil {
				return fmt.Errorf("unit: apply merge: move point %s: %w", p.PointID, err)
			}
			seen[key] = true
			moved++
			p.UnitID = survivorID
			s.indexPoint(&p)
		}
	}

	if newStart != survivor.LineStart || newEnd != survivor.LineEnd {
		if err := s.store.UpdateUnitBounds(survivorID, newStart, newEnd); err != nil {
			return fmt.Errorf("unit: apply merge: widen survivor bounds: %w", err)
		}
	}

	if err := s.SetUnitLifecycle(mergedIDs, LifecycleSuperseded, reason); err != nil {
		return fmt.Errorf("unit: apply merge: supersede merged units: %w", err)
	}

	// Reindex the survivor with its widened range and fresh markdown content.
	if ku, err := s.store.GetUnitByID(survivorID); err == nil {
		mdLines := s.loadSourceLines(ku.SourceID)
		s.indexUnit(ku, mdLines)
	}

	slog.Info("unit: offline duplicate merge applied",
		"survivor", survivorID, "merged", mergedIDs, "points_moved", moved,
		"new_range", []int{newStart, newEnd}, "reason", reason)
	return nil
}

// loadSourceLines best-effort reads a source's markdown lines for reindexing;
// an unreadable file just means the index doc keeps empty content.
func (s *Service) loadSourceLines(sourceID string) []string {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil || src.MarkdownPath == "" {
		return nil
	}
	data, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}
