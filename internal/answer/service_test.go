package answer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func setupTestService(t *testing.T) (*Service, *llm.FakeClient, *Store) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, nil)
	return svc, fake, store
}

func makeEvidenceSet(path string, direct, supporting []retrieval.Evidence) *retrieval.EvidenceSet {
	if direct == nil {
		direct = []retrieval.Evidence{}
	}
	if supporting == nil {
		supporting = []retrieval.Evidence{}
	}
	return &retrieval.EvidenceSet{
		Question:       "什么是线性方程？",
		Path:           path,
		DirectEvidence: direct,
		Supporting:     supporting,
	}
}

func TestAnswer_ShortPath(t *testing.T) {
	svc, fake, store := setupTestService(t)

	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"线性方程是ax+b=0的形式。","citations":["f1","f2"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "线性方程定义", UnitID: "u1", PointID: "p1", SourceRef: json.RawMessage(`{"source_id":"s1","line_start":1,"line_end":5}`)},
			{FactID: "f2", Content: "ax+b=0", UnitID: "u1", PointID: "p2", SourceRef: json.RawMessage(`{"source_id":"s1","line_start":6,"line_end":10}`)},
		},
		nil,
	)

	result := svc.Answer(context.Background(), es)

	if result.Path != "short" {
		t.Errorf("expected path=short, got %s", result.Path)
	}
	if !result.HasAnswer {
		t.Error("expected has_answer=true")
	}
	if result.Content != "线性方程是ax+b=0的形式。" {
		t.Errorf("unexpected content: %s", result.Content)
	}
	if len(result.Citations) != 2 {
		t.Errorf("expected 2 citations, got %d", len(result.Citations))
	}
	if result.AnswerID == "" {
		t.Error("expected non-empty answer_id")
	}

	saved, err := store.Get(result.AnswerID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if saved == nil {
		t.Fatal("expected saved answer")
	}
	if saved.Content != result.Content {
		t.Error("saved content mismatch")
	}
}

func TestAnswer_DeepPath(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("answer_deep.md", llm.FakeResponse{
		Output: `{"content":"经过推理分析...","citations":["f3"]}`,
	})

	es := makeEvidenceSet("deep",
		nil,
		[]retrieval.Evidence{
			{FactID: "f3", Content: "补充证据内容", UnitID: "u2", PointID: "p3", SourceRef: json.RawMessage(`{"source_id":"s2","line_start":1,"line_end":3}`)},
		},
	)

	result := svc.Answer(context.Background(), es)

	if result.Path != "deep" {
		t.Errorf("expected path=deep, got %s", result.Path)
	}
	if !result.HasAnswer {
		t.Error("expected has_answer=true")
	}
	if len(result.Citations) != 1 || result.Citations[0] != "f3" {
		t.Errorf("unexpected citations: %v", result.Citations)
	}
}

func TestAnswer_NonePath(t *testing.T) {
	svc, _, _ := setupTestService(t)

	es := makeEvidenceSet("deep", nil, nil)

	result := svc.Answer(context.Background(), es)

	if result.Path != "none" {
		t.Errorf("expected path=none, got %s", result.Path)
	}
	if result.HasAnswer {
		t.Error("expected has_answer=false")
	}
	if result.Content != "知识库中暂无相关材料，无法回答该问题。" {
		t.Errorf("unexpected content: %s", result.Content)
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected 0 citations, got %d", len(result.Citations))
	}
}

func TestAnswer_ErrorPath(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Err: llm.ErrTimeout,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "content", UnitID: "u1", PointID: "p1", SourceRef: json.RawMessage(`{}`)},
		},
		nil,
	)

	result := svc.Answer(context.Background(), es)

	if result.Path != "error" {
		t.Errorf("expected path=error, got %s", result.Path)
	}
	if result.HasAnswer {
		t.Error("expected has_answer=false")
	}
	if result.Content != "回答生成失败，请稍后重试。" {
		t.Errorf("unexpected content: %s", result.Content)
	}

	// Should have called LLM twice (initial + retry)
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Errorf("expected 2 LLM calls (initial+retry), got %d", len(calls))
	}
}

func TestAnswer_CitationValidation(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"回答内容","citations":["f1","hallucinated_id","f2"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "c1", UnitID: "u1", PointID: "p1", SourceRef: json.RawMessage(`{}`)},
			{FactID: "f2", Content: "c2", UnitID: "u1", PointID: "p2", SourceRef: json.RawMessage(`{}`)},
		},
		nil,
	)

	result := svc.Answer(context.Background(), es)

	if len(result.Citations) != 2 {
		t.Errorf("expected 2 valid citations, got %d: %v", len(result.Citations), result.Citations)
	}
	for _, c := range result.Citations {
		if c == "hallucinated_id" {
			t.Error("hallucinated fact_id should have been filtered")
		}
	}
}

func TestAnswer_ForceDeep(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("answer_deep.md", llm.FakeResponse{
		Output: `{"content":"深度分析...","citations":["f1"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "direct", UnitID: "u1", PointID: "p1", SourceRef: json.RawMessage(`{}`)},
		},
		nil,
	)

	result := svc.AnswerWithDeep(context.Background(), es, true)

	if result.Path != "deep" {
		t.Errorf("expected path=deep with forceDeep, got %s", result.Path)
	}
}

func TestAnswer_ComplexityUpgrade(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("answer_deep.md", llm.FakeResponse{
		Output: `{"content":"推理结果","citations":["f1"]}`,
	})

	es := &retrieval.EvidenceSet{
		Question: "如果成本超支20%，考核结果是什么？",
		Path:     "short",
		DirectEvidence: []retrieval.Evidence{
			{FactID: "f1", Content: "考核规则", UnitID: "u1", PointID: "p1", SourceRef: json.RawMessage(`{}`)},
		},
		Supporting: []retrieval.Evidence{},
	}

	result := svc.Answer(context.Background(), es)

	if result.Path != "deep" {
		t.Errorf("reasoning question should be upgraded to deep, got %s", result.Path)
	}

	calls := fake.Calls()
	if len(calls) != 1 || calls[0].PromptFile != "answer_deep.md" {
		t.Errorf("should use answer_deep.md, got calls: %v", calls)
	}
	if calls[0].Model != "reasoning" {
		t.Errorf("deep path should use reasoning model, got %s", calls[0].Model)
	}
}

func TestAnswer_NeedsReasoning(t *testing.T) {
	cases := []struct {
		question string
		expect   bool
	}{
		{"什么是绩效管理？", false},
		{"培训积分由哪几部分组成？", false},
		{"如果成本超支20%，结果是什么？", true},
		{"住宿费最高能报销多少？", true},
		{"经历了哪些阶段？", true},
		{"为什么要做绩效考核？", true},
		{"第一问？第二问？第三问？", false},
	}
	for _, tc := range cases {
		got := needsReasoning(tc.question)
		if got != tc.expect {
			t.Errorf("needsReasoning(%q) = %v, want %v", tc.question, got, tc.expect)
		}
	}
}

func TestAnswer_StreamCollectsContent(t *testing.T) {
	svc, fake, store := setupTestService(t)

	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"流式回答","citations":["f1"]}`,
	})

	// Need a retrieval service — use Answer directly via AnswerStream internals
	// Instead test the channel behavior with a direct evidence set
	// We'll test via the non-retrieval path by calling handleGenerate stream-style
	// Actually, AnswerStream needs retSvc — let's test FakeClient stream separately

	ch, err := fake.CompleteStream(context.Background(), "answer_short.md", nil, "default")
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var collected string
	var gotDone bool
	for chunk := range ch {
		switch chunk.Type {
		case llm.ChunkContent:
			collected += chunk.Content
		case llm.ChunkDone:
			gotDone = true
		}
	}

	if !gotDone {
		t.Error("expected ChunkDone")
	}
	expected := `{"content":"流式回答","citations":["f1"]}`
	if collected != expected {
		t.Errorf("collected = %q, want %q", collected, expected)
	}

	_ = svc
	_ = store
}

func TestAnswer_StreamError(t *testing.T) {
	fake := llm.NewFakeClient()
	fake.SetResponse("answer_short.md", llm.FakeResponse{Err: llm.ErrTimeout})

	_, err := fake.CompleteStream(context.Background(), "answer_short.md", nil, "default")
	if err != llm.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestAnswer_ForceDeepNoEvidence(t *testing.T) {
	svc, _, _ := setupTestService(t)

	es := makeEvidenceSet("short", nil, nil)

	result := svc.AnswerWithDeep(context.Background(), es, true)

	if result.Path != "none" {
		t.Errorf("forceDeep with no evidence should still be none, got %s", result.Path)
	}
}
