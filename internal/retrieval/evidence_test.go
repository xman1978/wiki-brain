package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/evidence"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

func evidenceTestConfig() config.EvidenceConfig {
	return config.EvidenceConfig{
		Enabled:           true,
		BatchMaxChars:     6000,
		MaxFragmentsPerKU: 5,
		MinFragmentChars:  4,
		Retry:             1,
	}
}

func TestBuildEvidenceSet_WithMining_ProducesFragmentLevelEvidence(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	svc.evidenceSvc = evidence.NewService(fake, evidenceTestConfig())

	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["Linear equations ax+b=0"]}]}`,
	})

	direct := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25},
	}

	es, err := svc.buildEvidenceSet(context.Background(), "what is a linear equation", "", "", "", "", "short", direct, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(es.DirectEvidence) != 1 {
		t.Fatalf("expected 1 mined fragment, got %d: %+v", len(es.DirectEvidence), es.DirectEvidence)
	}
	ev := es.DirectEvidence[0]
	if !ev.Mined {
		t.Error("expected mined=true")
	}
	if ev.Content != "Linear equations ax+b=0" {
		t.Errorf("content = %q", ev.Content)
	}
	if ev.FactID == "" {
		t.Error("expected a fact_id assigned")
	}
}

func TestBuildEvidenceSet_MiningDisabled_WholeSegmentUnchanged(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	cfg := evidenceTestConfig()
	cfg.Enabled = false
	svc.evidenceSvc = evidence.NewService(fake, cfg)

	direct := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25},
	}

	es, err := svc.buildEvidenceSet(context.Background(), "q", "", "", "", "", "short", direct, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(es.DirectEvidence) != 1 || es.DirectEvidence[0].Mined {
		t.Fatalf("expected whole-segment (mined=false) evidence, got %+v", es.DirectEvidence)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls when evidence.enabled=false, got %d", len(fake.Calls()))
	}
}

// TestRetrieve_FastPath_SkipsMining covers the fast-path latency cut:
// evidence mining (evidence_mine.md) is skipped on the fast path (docs/impl/
// v1/retrieval.md) — ActivationLink's direct hit is already history-verified
// and the extra LLM round trip was one of the dominant costs in a fast-path
// answer. DirectEvidence degrades to whole-segment (mined=false), the same
// fallback shape already used when mining fails, not a new degrade mode. No
// fake response is registered for evidence_mine.md, so the test would fail
// loudly (fake client error) if the fast path ever called it again. KPN
// expansion being skipped entirely on the fast path is covered separately by
// TestRetrieve_FastPath_NoKPNExpansion (fastpath_test.go).
func TestRetrieve_FastPath_SkipsMining(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.evidenceSvc = evidence.NewService(fake, evidenceTestConfig())

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast", es.PathType)
	}
	if len(es.DirectEvidence) != 1 || es.DirectEvidence[0].Mined {
		t.Fatalf("expected 1 whole-segment (mined=false) direct evidence item, got %+v", es.DirectEvidence)
	}
}
