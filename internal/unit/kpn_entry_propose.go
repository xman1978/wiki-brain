package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// conceptProposeBatchMaxSize caps how many orphan KPs go into one
// kpn_entry_propose.md LLM call (docs/impl/v1/kpn.md 步骤 3), mirroring
// crossBatchMaxSize.
const conceptProposeBatchMaxSize = 60

type conceptProposeCluster struct {
	SuggestedName        string `json:"suggested_name"`
	SuggestedDescription string `json:"suggested_description"`
	// Kind is the concept/fact classification (docs/impl/v1/kpn.md 步骤 3
	// "类型标注", 2026-08-04 新增) — concept: 底层理论/原理/规则；fact: 具体
	// 实现/技术/产品实例. Validated downstream by the concept module; an
	// empty or unrecognized value there defaults to "concept".
	Kind     string   `json:"kind"`
	PointIDs []string `json:"point_ids"`
}

type conceptProposeOutput struct {
	Clusters []conceptProposeCluster `json:"clusters"`
}

// ProposeEntriesForDomainOrphans is the 知识领域页面"+ 新增概念"按钮的入口
// (on-demand variant of docs/impl/v1/kpn.md 步骤 3, otherwise only run
// automatically at the end of each Source's unit_extract): clusters and
// names every standing entry_id-empty KP already in domainID — not just
// ones from a single just-imported Source — and hands each cluster to the
// concept evolution module as a pending_confirm candidate. Returns how many
// (cluster, source) proposals were written.
func (s *Service) ProposeEntriesForDomainOrphans(ctx context.Context, domainID string) (int, error) {
	if s.conceptNotifier == nil {
		return 0, nil
	}
	orphans, err := s.store.GetOrphanPointsByDomain(domainID)
	if err != nil {
		return 0, fmt.Errorf("unit: propose entries for domain orphans: get points: %w", err)
	}
	if len(orphans) == 0 {
		return 0, nil
	}

	unitIDs := make([]string, 0, len(orphans))
	for _, p := range orphans {
		unitIDs = append(unitIDs, p.UnitID)
	}
	unitCenterMap, err := s.store.GetUnitCentersByIDs(unitIDs)
	if err != nil {
		return 0, fmt.Errorf("unit: propose entries for domain orphans: get unit centers: %w", err)
	}

	return s.proposeEntriesForOrphans(ctx, "", domainID, orphans, unitCenterMap)
}

// proposeEntriesForOrphans implements docs/impl/v1/kpn.md 步骤 3: KUs with
// entry_id empty are chunked and named by the LLM, then handed to the
// concept evolution module's ProposeAddCandidate (via conceptNotifier),
// which merges into an existing pending content_driven candidate for the
// same domain or creates a new one — never building any cross-Source
// relation for these points in this pass. orphans may span multiple Sources
// (the on-demand domain-wide trigger, unlike the per-Source KPN pipeline
// call, does) — each cluster's point_ids are grouped by their own SourceID
// before calling ProposeAddCandidate, one call per (cluster, source) pair,
// so evidence.source_ids ends up complete via the existing merge path
// instead of needing a wider notifier signature. Returns how many
// (cluster, source) proposals were successfully written. Failures degrade
// per-chunk: a bad LLM call or write only drops that chunk (retried next
// time), never the whole run.
func (s *Service) proposeEntriesForOrphans(ctx context.Context, logSourceID, domainID string, orphans []KnowledgePoint, unitCenterMap map[string]string) (int, error) {
	if s.conceptNotifier == nil || len(orphans) == 0 {
		return 0, nil
	}

	existingNames, err := s.conceptNotifier.ListActiveEntryNames(domainID)
	if err != nil {
		slog.Warn("unit: kpn concept propose existing entries lookup failed", "domain_id", domainID, "error", err)
	}
	existingNamesText := strings.Join(existingNames, "、")

	touched := 0
	for start := 0; start < len(orphans); start += conceptProposeBatchMaxSize {
		end := start + conceptProposeBatchMaxSize
		if end > len(orphans) {
			end = len(orphans)
		}
		chunk := orphans[start:end]

		var text strings.Builder
		pointSourceMap := make(map[string]string, len(chunk))
		for _, p := range chunk {
			pointSourceMap[p.PointID] = p.SourceID
			fmt.Fprintf(&text, "%s\t%s\t%s\n", p.PointID, unitCenterMap[p.UnitID], p.Content)
		}

		vars := map[string]string{"points": text.String(), "existing_concepts": existingNamesText}
		data, err := s.llmClient.CompleteJSON(ctx, "kpn_entry_propose.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kpn concept propose llm call failed", "domain_id", domainID, "error", err)
			continue
		}
		var out conceptProposeOutput
		if err := json.Unmarshal(data, &out); err != nil {
			slog.Warn("unit: kpn concept propose parse failed", "domain_id", domainID, "error", err)
			continue
		}

		for _, cluster := range out.Clusters {
			if cluster.SuggestedName == "" {
				continue
			}
			bySource := make(map[string][]string)
			for _, pid := range cluster.PointIDs {
				if srcID, ok := pointSourceMap[pid]; ok {
					bySource[srcID] = append(bySource[srcID], pid)
				}
			}
			if len(bySource) == 0 {
				continue
			}

			for srcID, pointIDs := range bySource {
				if _, err := s.conceptNotifier.ProposeAddCandidate(domainID, cluster.SuggestedName, cluster.SuggestedDescription, cluster.Kind, pointIDs, srcID); err != nil {
					slog.Warn("unit: kpn concept propose candidate write failed", "source_id", srcID, "suggested_name", cluster.SuggestedName, "error", err)
					continue
				}
			}
			touched++
		}
	}

	slog.Info("unit: kpn orphan concept proposal done",
		"source_id", logSourceID, "domain_id", domainID, "orphan_points", len(orphans), "proposals_touched", touched)
	return touched, nil
}
