package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// setupTestServiceWithActivation reuses setupTestService's fixture (u1/p1,
// u2/p2, u3/p3, u4/p4 — see store_test.go's seedTestData and
// service_test.go's Bleve/markdown seeding) and wires a real
// activation.Service against the same DB, so ActivationLink Match() can be
// exercised end-to-end through Retrieve.
func setupTestServiceWithActivation(t *testing.T) (*Service, *llm.FakeClient, *Store, *activation.Service) {
	t.Helper()
	svc, fake, store := setupTestService(t)
	activationStore := activation.NewStore(store.db)
	activationSvc := activation.NewService(activationStore, activation.NewMatcher(activationStore))
	svc.activationSvc = activationSvc
	svc.cfg.Retrieval.FastPath = true
	svc.cfg.Retrieval.FastPathFallback = true
	return svc, fake, store, activationSvc
}

// seedVerifiedLink creates and immediately verifies an ActivationLink with no
// four-tuple condition, so the matcher's fallback path (question_terms
// overlap) is what matches it.
func seedVerifiedLink(t *testing.T, activationSvc *activation.Service, questionTerms, pointID string) *activation.ActivationLink {
	t.Helper()
	link, err := activationSvc.CreateLink(questionTerms, activation.LinkCondition{}, pointID, nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	updated, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil)
	if err != nil {
		t.Fatalf("verify link: %v", err)
	}
	return updated
}

func TestRetrieve_FastPath_Hit(t *testing.T) {
	svc, _, _, activationSvc := setupTestServiceWithActivation(t)
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
	if es.Path != "short" {
		t.Errorf("path = %q, want short", es.Path)
	}
	if len(es.DirectEvidence) != 1 || es.DirectEvidence[0].UnitID != "u1" {
		t.Fatalf("expected direct evidence from u1, got %+v", es.DirectEvidence)
	}
	if len(es.ActivationHits) != 1 || es.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected 1 activation hit for p1, got %+v", es.ActivationHits)
	}
}

func TestRetrieve_FastPath_KPNExpansion(t *testing.T) {
	svc, _, _, activationSvc := setupTestServiceWithActivation(t)
	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// p1's KPN neighbors are p2 (related/bidirectional) and p3 (supplements/directed).
	supportingUnits := make(map[string]bool)
	for _, e := range es.Supporting {
		supportingUnits[e.UnitID] = true
	}
	if !supportingUnits["u2"] || !supportingUnits["u3"] {
		t.Errorf("expected u2 and u3 as KPN supporting evidence, got %+v", es.Supporting)
	}
}

func TestRetrieve_FastPathDisabled_StillRecordsHitsButUsesSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.cfg.Retrieval.FastPath = false

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"outline_ids": ["o2"]}`})
	fake.SetResponse("rerank_extract.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "source_theme": "linear equations", "content_theme": "linear equations definition", "intent": "说明定义", "object": "linear equations", "scope": "通用", "key_facts": ["linear equations definition"]}]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (fast_path disabled)", es.PathType)
	}
	if len(es.ActivationHits) != 1 || es.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected activation_hits still recorded for gated rollout observability, got %+v", es.ActivationHits)
	}
}

func TestRetrieve_ForceFull_SkipsFastPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"outline_ids": ["o2"]}`})
	fake.SetResponse("rerank_extract.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "source_theme": "linear equations", "content_theme": "linear equations definition", "intent": "说明定义", "object": "linear equations", "scope": "通用", "key_facts": ["linear equations definition"]}]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question, ForceFull: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (force_full)", es.PathType)
	}
	if len(es.ActivationHits) != 0 {
		t.Errorf("expected no activation_hits when force_full skips matching entirely, got %+v", es.ActivationHits)
	}
}

func TestRetrieve_NoMatch_FallsBackToSlowPathWithEmptyHits(t *testing.T) {
	svc, fake, _, _ := setupTestServiceWithActivation(t)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"outline_ids": ["o2"]}`})
	fake.SetResponse("rerank_extract.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "source_theme": "linear equations", "content_theme": "linear equations definition", "intent": "说明定义", "object": "linear equations", "scope": "通用", "key_facts": ["linear equations definition"]}]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: "linear equations"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full", es.PathType)
	}
	if len(es.ActivationHits) != 0 {
		t.Errorf("expected no activation_hits without a matching link, got %+v", es.ActivationHits)
	}
}

func TestRetrieveSlowPathWithProgress_BypassesFastPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"outline_ids": ["o2"]}`})
	fake.SetResponse("rerank_extract.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "source_theme": "linear equations", "content_theme": "linear equations definition", "intent": "说明定义", "object": "linear equations", "scope": "通用", "key_facts": ["linear equations definition"]}]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveSlowPathWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full", es.PathType)
	}
}

func TestRetrieve_FastPath_NoCurrentKP_FallsBackToSlowPath(t *testing.T) {
	svc, fake, store, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	// KP superseded after Match() would have found it verified, but before
	// GetCurrentUnitsByPointIDs resolves it — the matcher itself already
	// excludes non-current-KP links (ListVerifiedLinksForCurrentKP), so this
	// also verifies Match() returns nothing once p1 is no longer current.
	store.db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'p1'`)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"outline_ids": ["o2"]}`})
	fake.SetResponse("rerank_extract.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "source_theme": "linear equations", "content_theme": "linear equations definition", "intent": "说明定义", "object": "linear equations", "scope": "通用", "key_facts": ["linear equations definition"]}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (matched link's KP no longer current)", es.PathType)
	}
}
