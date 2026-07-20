package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// conceptProposeBatchMaxSize caps how many orphan KPs go into one
// kpn_concept_propose.md LLM call (docs/impl/v1/kpn.md 步骤 3), mirroring
// crossBatchMaxSize.
const conceptProposeBatchMaxSize = 60

type conceptProposeOutput struct {
	SuggestedName        string `json:"suggested_name"`
	SuggestedDescription string `json:"suggested_description"`
}

// proposeConceptsForOrphans implements docs/impl/v1/kpn.md 步骤 3: KUs with
// concept_id empty are chunked and named by the LLM, then handed to the
// concept evolution module's ProposeAddCandidate (via conceptNotifier),
// which merges into an existing pending content_driven candidate for the
// same domain or creates a new one — never building any cross-Source
// relation for these points in this pass. Returns how many chunks were successfully
// proposed. Failures degrade per-chunk: a bad LLM call or write only drops
// that chunk (retried on the domain's next import), never the whole Source.
func (s *Service) proposeConceptsForOrphans(ctx context.Context, sourceID, domainID string, orphans []KnowledgePoint, unitCenterMap map[string]string) (int, error) {
	if s.conceptNotifier == nil || len(orphans) == 0 {
		return 0, nil
	}

	touched := 0
	for start := 0; start < len(orphans); start += conceptProposeBatchMaxSize {
		end := start + conceptProposeBatchMaxSize
		if end > len(orphans) {
			end = len(orphans)
		}
		chunk := orphans[start:end]

		var text strings.Builder
		pointIDs := make([]string, 0, len(chunk))
		for _, p := range chunk {
			pointIDs = append(pointIDs, p.PointID)
			fmt.Fprintf(&text, "%s\t%s\t%s\n", p.PointID, unitCenterMap[p.UnitID], p.Content)
		}

		vars := map[string]string{"points": text.String()}
		data, err := s.llmClient.CompleteJSON(ctx, "kpn_concept_propose.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kpn concept propose llm call failed", "source_id", sourceID, "domain_id", domainID, "error", err)
			continue
		}
		var out conceptProposeOutput
		if err := json.Unmarshal(data, &out); err != nil {
			slog.Warn("unit: kpn concept propose parse failed", "source_id", sourceID, "error", err)
			continue
		}
		if out.SuggestedName == "" {
			slog.Warn("unit: kpn concept propose empty suggested_name, skipping chunk", "source_id", sourceID, "point_count", len(chunk))
			continue
		}

		if _, err := s.conceptNotifier.ProposeAddCandidate(domainID, out.SuggestedName, out.SuggestedDescription, pointIDs, sourceID); err != nil {
			slog.Warn("unit: kpn concept propose candidate write failed", "source_id", sourceID, "error", err)
			continue
		}
		touched++
	}

	slog.Info("unit: cross kpn orphan concept proposal done",
		"source_id", sourceID, "domain_id", domainID, "orphan_points", len(orphans), "proposals_touched", touched)
	return touched, nil
}
