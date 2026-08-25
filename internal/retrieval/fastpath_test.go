package retrieval

import (
	"context"
	"sort"
	"testing"
	"time"

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
	matcher := activation.NewMatcher(activationStore)
	activationSvc := activation.NewService(activationStore, matcher)
	svc.activationSvc = activationSvc
	svc.cfg.Retrieval.FastPath = true
	svc.cfg.Retrieval.FastPathFallback = true
	return svc, fake, store, activationSvc
}

// seedVerifiedLink creates an ActivationLink with no four-tuple condition, so
// the matcher's empty-observed-conditions fallback path (question_terms
// overlap) is what matches it — that branch is explicitly tier-exempt
// (docs/impl/v1/activation.md「回退（observed_conditions 为空）」: "命中后
// 直接判定 score=1.0，不经过置信度分档"), and classifyActivationMatches no
// longer filters by status=verified (2026-08-13, Match() itself already
// decided per-condition whether this round should serve), so this link
// participates in the fast path regardless of its (candidate) status —
// no TransitionLink call needed post-2026-08-13.
func seedVerifiedLink(t *testing.T, activationSvc *activation.Service, questionTerms, pointID string) *activation.ActivationLink {
	t.Helper()
	link, err := activationSvc.CreateLink(questionTerms, activation.LinkCondition{}, pointID, nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	return link
}

func TestRetrieve_FastPath_Hit(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "kpn neighbor"}, {"candidate_id": "c2", "relevant": true, "analysis": "kpn neighbor"}]}`})

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

// TestRetrieve_FastPath_NoKPNExpansion confirms the fast path no longer
// expands into a matched point's KPN neighbors at all (docs/impl/v1/
// retrieval.md): ActivationLink/Bundle's point_id is itself the
// history-verified relevant KP, so a KPN "related" neighbor is trusted only
// if it earns its own ActivationLink — not pulled in ad hoc via the graph on
// every fast-path answer. Neither rerank_relevance.md nor rerank_classify.md
// should be called, and Supporting must be empty.
func TestRetrieve_FastPath_NoKPNExpansion(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(es.Supporting) != 0 {
		t.Errorf("expected no KPN supporting evidence on fast path, got %+v", es.Supporting)
	}
	for _, c := range fake.Calls() {
		if c.PromptFile == "rerank_classify.md" || c.PromptFile == "rerank_relevance.md" {
			t.Errorf("fast path must not call KPN judge prompts, saw %q", c.PromptFile)
		}
	}
}

// TestRetrieve_FastPath_MultipleUnits_FallsBackToSlowPath: >1 distinct KU
// among verified matches is ambiguity (no ranking at score=1.0) → full.
// p1→u1 and p2→u2 are different units in seedTestData.
func TestRetrieve_FastPath_MultipleUnits_FallsBackToSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p2")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (multi-unit activation match must not use the fast path)", es.PathType)
	}
	if len(es.ActivationHits) != 2 {
		t.Fatalf("expected both ambiguous matches recorded in activation_hits, got %+v", es.ActivationHits)
	}
}

// TestRetrieve_FastPath_MultipleLinksSameUnit_TakesFastPath: two verified
// links whose KPs share one KU are not ambiguous — fast path may answer
// from that single unit body.
func TestRetrieve_FastPath_MultipleLinksSameUnit_TakesFastPath(t *testing.T) {
	svc, fake, store, activationSvc := setupTestServiceWithActivation(t)

	if _, err := store.db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p1b', 'u1', 's1', 'another fact on same unit', 'fact')`); err != nil {
		t.Fatalf("insert p1b: %v", err)
	}

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p1b")

	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "kpn neighbor"}, {"candidate_id": "c2", "relevant": true, "analysis": "kpn neighbor"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast (same-unit multi-link)", es.PathType)
	}
	if len(es.ActivationHits) != 2 {
		t.Fatalf("expected both hits recorded, got %+v", es.ActivationHits)
	}
	if len(es.DirectEvidence) != 1 || es.DirectEvidence[0].UnitID != "u1" {
		t.Fatalf("expected single direct unit u1, got %+v", es.DirectEvidence)
	}
}

func TestRetrieve_FastPathDisabled_StillRecordsHitsButUsesSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.cfg.Retrieval.FastPath = false

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

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
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

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
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

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
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveSlowPathWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full", es.PathType)
	}
}

func TestRetrieve_FastPathVerify_Sufficient_KeepsFastPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.cfg.Retrieval.FastPathVerify = true

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("fast_verify.md", llm.FakeResponse{Output: `{"sufficient": true, "reason": "证据已给出完整定义"}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "kpn neighbor"}, {"candidate_id": "c2", "relevant": true, "analysis": "kpn neighbor"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast (verify judged sufficient)", es.PathType)
	}
}

func TestRetrieve_FastPathVerify_Insufficient_FallsBackToSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.cfg.Retrieval.FastPathVerify = true

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("fast_verify.md", llm.FakeResponse{Output: `{"sufficient": false, "reason": "证据未覆盖问题的完整诉求"}`})
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (verify judged insufficient)", es.PathType)
	}
	if len(es.ActivationHits) != 1 || es.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected original activation_hits preserved on fallback ES, got %+v", es.ActivationHits)
	}
}

// TestRetrieve_FastPathVerify_MalformedResponse_FallsBackToSlowPath covers V3
// from the acceptance test plan's P3 axis 2 (docs/impl/v1/retrieval.md 步骤
// 2a "保守回落"): a real LLM occasionally times out or returns unparseable
// output, and this can't be forced deterministically against a real model in
// the black-box acceptance suite, so it's covered here with FakeClient
// instead. Any verify-call failure — not just an explicit sufficient=false —
// must fall back rather than risk answering from unverified evidence.
func TestRetrieve_FastPathVerify_MalformedResponse_FallsBackToSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	svc.cfg.Retrieval.FastPathVerify = true

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	fake.SetResponse("fast_verify.md", llm.FakeResponse{Output: `not valid json`})
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据语义说明线性方程定义，可直接回答问题"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (malformed verify response must fall back, not error out)", es.PathType)
	}
	if len(es.ActivationHits) != 1 || es.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected original activation_hits preserved on fallback ES, got %+v", es.ActivationHits)
	}
}

func TestRetrieve_FastPath_NoCurrentKP_FallsBackToSlowPath(t *testing.T) {
	svc, fake, store, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	// KP superseded after Match() would have found it verified, but before
	// GetCurrentUnitsByPointIDs resolves it — the matcher itself already
	// excludes non-current-KP links (ListMatchableLinksForCurrentKP), so this
	// also verifies Match() returns nothing once p1 is no longer current.
	store.db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'p1'`)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("path_type = %q, want full (matched link's KP no longer current)", es.PathType)
	}
}

// TestRetrieve_ExploringTierCondition_RecordsHitButFallsBackToFull covers
// the 2026-08-13 replacement for the old "candidate must not take fast path"
// invariant (docs/design/activation-convergence.md): status is no longer
// what gates the fast path — Match()'s own per-condition tiering is. A
// condition whose confidence is still low (exploring tier) and isn't
// sampled in this round (explore_rate_low=0) records an ActivationHit (for
// Trace) but doesn't serve — same externally-observable "candidate not
// served" behavior as before, driven by a different, continuous mechanism.
func TestRetrieve_ExploringTierCondition_RecordsHitButFallsBackToFull(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	activationSvc.SetConfidenceConfig(activation.ConfidenceConfig{
		ServingConfidenceMin: 0.9, AuditSampleMin: 5, ExploreRateLow: 0,
	})

	question := "什么是线性方程"
	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{SubjectTerms: "线性方程"}, "p1", nil)
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	if link.Status != activation.StatusCandidate {
		t.Fatalf("status = %q, want candidate", link.Status)
	}

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "matches"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question, Subject: "线性方程"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Fatalf("path_type = %q, want full (exploring-tier condition not sampled in must not take fast path)", es.PathType)
	}
	if len(es.ActivationHits) != 0 {
		t.Fatalf("expected 0 activation hits — exploring tier not sampled means Match() produces no hit at all this round, got %+v", es.ActivationHits)
	}
	_ = link
}

// TestRetrieve_SubjectJitter_MissesFastPath_FallsBackToFull: 2026-08-12 修订
// — subject 不再做同义词/包含模糊匹配，round 2 模型辅助判断整体移除，一次
// 抖动的 subject 措辞不再能命中 Match，只能走慢路径。替换了原先验证 round 2
// 能救回这种抖动的 TestRetrieve_ModelAssistedMatch_RescuesSubjectJitter。
func TestRetrieve_SubjectJitter_MissesFastPath_FallsBackToFull(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	now := time.Now().UTC()
	cond := activation.NormalizeObservedCondition(
		"数据库连接句柄异常", "查询解决方法", "", "达梦", "", "", "", now,
	)
	link, err := activationSvc.CreateLink("句柄 异常", activation.LinkCondition{
		ObservedConditions: []activation.ObservedCondition{cond},
	}, "p1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	_ = link

	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "x"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "x"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{
		Question:       "达梦报语句句柄个数超过上限怎么解决？",
		Subject:        "数据库连接句柄超限", // jittered — same intent/audience/constraint, different subject wording
		Intent:         "查询解决方法",
		Audience:       "",
		Constraint:     "达梦",
		DomainIDs:      []string{"d1"},
		DomainResolved: true,
		FollowUp:       false,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Fatalf("path_type = %q, want full — jittered subject no longer matches", es.PathType)
	}
}

// ── Bundle-driven multi-KP hit resolution (docs/impl/v1/retrieval.md 步骤 2's
// Bundle-consultation branch, docs/impl/v1/activation-bundle.md 步骤 4b) ──────

// TestRetrieve_FastPath_MultipleUnits_FormsCandidateBundle: extends the
// baseline ambiguous-multi-unit case — no verified bundle covers p1/p2, so
// falling back to slow path must have a side effect: a new candidate bundle
// seeded from this observation, so future identical hits have Bundle
// material to Match against.
// TestRetrieve_FastPath_MultipleUnits_NoBundleCovers_FallsBackWithoutSideEffect
// 2026-08-20 重设计：两条 Link 各自独立命中同一问题、但从未在同一条 trace
// 里共同被引用过，是比"直接联合引用"更弱的信号（可能是互相替代关系，不是
// 联合必需）——不再直接拿来建候选 Bundle（docs/design/activation-bundle.md
// 改判），只落回慢路径；即使这个歧义反复出现多次，也不应该产生任何 Bundle
// 副作用，真正的联合引用证据留给 Study 从慢路径产生的真实 confident trace
// 里正常显影（bundle_scan.go）。
func TestRetrieve_FastPath_MultipleUnits_NoBundleCovers_FallsBackWithoutSideEffect(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p2")

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "x"}]}`})

	for i := 0; i < 2; i++ {
		es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
		if err != nil {
			t.Fatalf("retrieve #%d: %v", i+1, err)
		}
		if es.PathType != PathTypeFull {
			t.Fatalf("retrieve #%d: path_type = %q, want full (no bundle covers the ambiguous hit)", i+1, es.PathType)
		}
	}

	bundles, err := activationSvc.Store().ListMatchableBundles(nil)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected no bundle to be formed from independently-matched Links, got %d: %+v", len(bundles), bundles)
	}
}

// TestRetrieve_FastPath_VerifiedBundleCoversAmbiguousHit_TakesFastPath: a
// verified bundle whose core members are exactly the ambiguous hit's points,
// with observed_conditions matching this turn's four-tuple, lets the fast
// path answer directly instead of falling back.
func TestRetrieve_FastPath_VerifiedBundleCoversAmbiguousHit_TakesFastPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p2")

	now := time.Now().UTC()
	cond := activation.NormalizeObservedCondition("线性方程", "定义", "", "", "", "", qTerms, now)
	bundle := &activation.ActivationBundle{
		RepresentativeTerms: "线性方程 定义",
		ObservedConditions:  []activation.ObservedCondition{cond},
		Members:             []activation.BundleMember{{PointID: "p1"}, {PointID: "p2"}},
	}
	if err := activationSvc.Store().CreateBundle(bundle); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundle.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}

	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "x"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{
		Question: question,
		Subject:  "线性方程",
		Intent:   "定义",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast (verified bundle covers the ambiguous multi-unit hit)", es.PathType)
	}
}

// TestRetrieve_FastPath_BundleResolved_PopulatesBundleHits covers the
// 2026-08-20「验证」阶段 2 接线: when a Bundle resolves the fast path, the
// resulting EvidenceSet carries a BundleHits entry with the matched
// condition's own quadruple and the member point_ids actually used — the
// data trace.recordBundleHitOutcome needs to call RecordBundleOutcome/
// RecordMemberOutcome. Before this change usedBundleIDs was computed but
// discarded, so es.BundleHits was always empty.
func TestRetrieve_FastPath_BundleResolved_PopulatesBundleHits(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p2")

	now := time.Now().UTC()
	cond := activation.NormalizeObservedCondition("线性方程", "定义", "", "", "", "", qTerms, now)
	bundle := &activation.ActivationBundle{
		RepresentativeTerms: "线性方程 定义",
		ObservedConditions:  []activation.ObservedCondition{cond},
		Members:             []activation.BundleMember{{PointID: "p1"}, {PointID: "p2"}},
	}
	if err := activationSvc.Store().CreateBundle(bundle); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundle.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}

	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "x"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{
		Question: question,
		Subject:  "线性方程",
		Intent:   "定义",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast", es.PathType)
	}
	if len(es.BundleHits) != 1 {
		t.Fatalf("expected 1 BundleHit, got %d: %+v", len(es.BundleHits), es.BundleHits)
	}
	hit := es.BundleHits[0]
	if hit.BundleID != bundle.BundleID {
		t.Errorf("bundle_id = %q, want %q", hit.BundleID, bundle.BundleID)
	}
	if hit.Subject != "线性方程" || hit.Intent != "定义" {
		t.Errorf("unexpected matched quadruple: %+v", hit)
	}
	gotMembers := append([]string(nil), hit.MemberPointIDs...)
	sort.Strings(gotMembers)
	if len(gotMembers) != 2 || gotMembers[0] != "p1" || gotMembers[1] != "p2" {
		t.Errorf("member_point_ids = %v, want [p1 p2]", hit.MemberPointIDs)
	}
}

// TestRetrieve_FastPath_MultipleVerifiedBundles_NoConflict_MergesAndTakesFastPath:
// two verified bundles matched independently, whose core members have no
// contradicts relation between them (p1↔p2 is 'related' per seedTestData) —
// merge into one point set, still fast path, no new bundle created.
func TestRetrieve_FastPath_MultipleVerifiedBundles_NoConflict_MergesAndTakesFastPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p2")

	now := time.Now().UTC()
	condA := activation.NormalizeObservedCondition("线性方程", "定义", "", "", "", "", qTerms, now)
	bundleA := &activation.ActivationBundle{RepresentativeTerms: "A", ObservedConditions: []activation.ObservedCondition{condA}, Members: []activation.BundleMember{{PointID: "p1"}}}
	if err := activationSvc.Store().CreateBundle(bundleA); err != nil {
		t.Fatalf("create bundle A: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundleA.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle A: %v", err)
	}
	bundleB := &activation.ActivationBundle{RepresentativeTerms: "B", ObservedConditions: []activation.ObservedCondition{condA}, Members: []activation.BundleMember{{PointID: "p2"}}}
	if err := activationSvc.Store().CreateBundle(bundleB); err != nil {
		t.Fatalf("create bundle B: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundleB.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle B: %v", err)
	}

	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "x"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{
		Question: question,
		Subject:  "线性方程",
		Intent:   "定义",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast (two non-conflicting verified bundles merged)", es.PathType)
	}

	bundles, err := activationSvc.Store().ListMatchableBundles(nil)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("expected still exactly the 2 pre-seeded bundles, no new one created by the merge, got %d", len(bundles))
	}
}

// TestRetrieve_FastPath_ConflictingVerifiedBundles_FallsBackToSlowPath: two
// verified bundles whose core members DO have a contradicts KPN relation
// (p1↔p4 per seedTestData) must not be merged — ambiguous, slow path.
func TestRetrieve_FastPath_ConflictingVerifiedBundles_FallsBackToSlowPath(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	seedVerifiedLink(t, activationSvc, qTerms, "p4")

	now := time.Now().UTC()
	cond := activation.NormalizeObservedCondition("线性方程", "定义", "", "", "", "", qTerms, now)
	bundleA := &activation.ActivationBundle{RepresentativeTerms: "A", ObservedConditions: []activation.ObservedCondition{cond}, Members: []activation.BundleMember{{PointID: "p1"}}}
	if err := activationSvc.Store().CreateBundle(bundleA); err != nil {
		t.Fatalf("create bundle A: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundleA.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle A: %v", err)
	}
	bundleB := &activation.ActivationBundle{RepresentativeTerms: "B", ObservedConditions: []activation.ObservedCondition{cond}, Members: []activation.BundleMember{{PointID: "p4"}}}
	if err := activationSvc.Store().CreateBundle(bundleB); err != nil {
		t.Fatalf("create bundle B: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(bundleB.BundleID, activation.StatusVerified); err != nil {
		t.Fatalf("verify bundle B: %v", err)
	}

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "x"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "x"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{
		Question: question,
		Subject:  "线性方程",
		Intent:   "定义",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFull {
		t.Fatalf("path_type = %q, want full (conflicting bundles must not merge)", es.PathType)
	}
}
