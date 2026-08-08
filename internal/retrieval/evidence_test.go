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

	es, err := svc.buildEvidenceSet(context.Background(), "what is a linear equation", "", "", "", "", "short", direct, nil, nil, nil, false)
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

	es, err := svc.buildEvidenceSet(context.Background(), "q", "", "", "", "", "short", direct, nil, nil, nil, false)
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

func TestRetrieve_FastPath_WithMining(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.evidenceSvc = evidence.NewService(fake, evidenceTestConfig())

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	// The matched link's KP (p1) has KPN neighbors (p2, p3 — see seedTestData),
	// which must now clear the rerank judge (judgeKPNExpansion) before being
	// trusted as supporting candidates; cover all of them so the coverage
	// check doesn't treat this as a batch failure.
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "supporting", "analysis": "kpn neighbor"}, {"candidate_id": "c2", "role": "supporting", "analysis": "kpn neighbor"}]}`})
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [
			{"candidate_id": "c1", "fragments": ["Linear equations ax+b=0"]},
			{"candidate_id": "c2", "fragments": []},
			{"candidate_id": "c3", "fragments": []}
		]}`,
	})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast", es.PathType)
	}
	if len(es.DirectEvidence) != 1 || !es.DirectEvidence[0].Mined {
		t.Fatalf("expected 1 mined direct evidence item, got %+v", es.DirectEvidence)
	}
	if es.DirectEvidence[0].Content != "Linear equations ax+b=0" {
		t.Errorf("content = %q", es.DirectEvidence[0].Content)
	}
}
