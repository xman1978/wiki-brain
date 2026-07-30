package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// TestSkeletonInjection_FullBypassSkipsDomainAndSourcePrefilter implements
// docs/impl/v1/wiki.md 完成标准: "主题页展开后直答全失败时，慢路径未执行
// Outline 召回...LLM 调用数不高于同问题无主题页命中时" — the concrete,
// checkable form of that here is decision A's full-bypass branch: when
// skeleton_point_ids >= rerank_top_n, Domain pre-filter (question_domain_match.md)
// and Source semantic filter (source_filter.md) must never be called, since
// Outline/FTS recall — the thing those two exist to narrow — is skipped
// entirely too.
func TestSkeletonInjection_FullBypassSkipsDomainAndSourcePrefilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	svc.cfg.Retrieval.SkeletonInjectionEnabled = true
	svc.cfg.Retrieval.RerankTopN = 2 // skeleton below will supply exactly 2 points

	fake.SetResponse("rerank_judge.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "ok"}, {"candidate_id": "c2", "role": "supporting", "analysis": "ok"}]}`,
	})

	skeletonMembers := []SkeletonMemberInfo{
		{PageID: "concept-page-1", PointIDs: []string{"p1", "p2"}},
	}

	qc := QueryContext{Question: "what is linear equation?"}
	es, err := svc.retrieveSlowPathWithSkeleton(context.Background(), qc, nil, "topic-page-1", skeletonMembers)
	if err != nil {
		t.Fatalf("retrieveSlowPathWithSkeleton: %v", err)
	}

	for _, c := range fake.Calls() {
		if c.PromptFile == "question_domain_match.md" || c.PromptFile == "source_filter.md" {
			t.Errorf("expected Domain/Source prefilter to be skipped on full-bypass, but %q was called", c.PromptFile)
		}
	}

	sawRerank := false
	for _, c := range fake.Calls() {
		if c.PromptFile == "rerank_judge.md" {
			sawRerank = true
		}
	}
	if !sawRerank {
		t.Error("expected rerank_judge.md to be called (candidates went straight to Rerank)")
	}

	if es.SkeletonPageID != "topic-page-1" {
		t.Errorf("expected SkeletonPageID to be tagged onto the result, got %q", es.SkeletonPageID)
	}
	if es.PathType != PathTypeFull {
		t.Errorf("expected path_type=full, got %q", es.PathType)
	}
}

// TestSkeletonInjection_BelowThresholdKeepsPrefilter implements decision A's
// supplement branch: when skeleton candidates are fewer than rerank_top_n,
// Domain/Source prefilter still runs (skeleton only supplements outline/FTS,
// doesn't replace them).
func TestSkeletonInjection_BelowThresholdKeepsPrefilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	svc.cfg.Retrieval.SkeletonInjectionEnabled = true
	svc.cfg.Retrieval.RerankTopN = 20 // skeleton (1 point) stays well under this

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "ok"}]}`,
	})

	skeletonMembers := []SkeletonMemberInfo{{PageID: "concept-page-1", PointIDs: []string{"p1"}}}

	qc := QueryContext{Question: "what is linear equation?"}
	es, err := svc.retrieveSlowPathWithSkeleton(context.Background(), qc, nil, "topic-page-1", skeletonMembers)
	if err != nil {
		t.Fatalf("retrieveSlowPathWithSkeleton: %v", err)
	}

	sawDomain, sawSource := false, false
	for _, c := range fake.Calls() {
		if c.PromptFile == "question_domain_match.md" {
			sawDomain = true
		}
		if c.PromptFile == "source_filter.md" {
			sawSource = true
		}
	}
	if !sawDomain || !sawSource {
		t.Errorf("expected Domain/Source prefilter to still run below threshold, sawDomain=%v sawSource=%v", sawDomain, sawSource)
	}
	if es.SkeletonPageID != "topic-page-1" {
		t.Errorf("expected SkeletonPageID to be tagged, got %q", es.SkeletonPageID)
	}
}

// TestSkeletonInjection_GateOffStillTagsObservability implements decision B:
// the gate only controls actual candidate injection — skeleton_page_id
// should still be recorded when the gate is off, since study.md 步骤 7's
// skeleton_used_count needs data before the gate can responsibly be flipped
// on.
func TestSkeletonInjection_GateOffStillTagsObservability(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	svc.cfg.Retrieval.SkeletonInjectionEnabled = false

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "ok"}]}`,
	})

	skeletonMembers := []SkeletonMemberInfo{{PageID: "concept-page-1", PointIDs: []string{"p1"}}}
	qc := QueryContext{Question: "what is linear equation?"}
	es, err := svc.retrieveSlowPathWithSkeleton(context.Background(), qc, nil, "topic-page-1", skeletonMembers)
	if err != nil {
		t.Fatalf("retrieveSlowPathWithSkeleton: %v", err)
	}
	if es.SkeletonPageID != "topic-page-1" {
		t.Errorf("expected SkeletonPageID still tagged with gate off, got %q", es.SkeletonPageID)
	}
}
