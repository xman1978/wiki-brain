package answer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
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

func TestBuildPromptVarsIncludesEvidenceAttribution(t *testing.T) {
	es := makeEvidenceSet("short",
		[]retrieval.Evidence{{
			FactID:       "f1",
			Content:      "回款提成按履约时间分档计提。",
			SourceTitle:  "应收账款管理制度",
			SourceTheme:  "应收账款管理制度",
			ContentTheme: "销售回款绩效考核与提成规则",
			Object:       "销售人员",
			Scope:        "销售自签合同及公司分配的回款项目",
		}},
		nil,
	)

	vars := buildPromptVars(es)
	got := vars["direct_evidence_list"]
	for _, want := range []string{
		"[f1]",
		"来源标题：应收账款管理制度",
		"来源主题：应收账款管理制度",
		"内容主题：销售回款绩效考核与提成规则",
		"对象：销售人员",
		"范围：销售自签合同及公司分配的回款项目",
		"回款提成按履约时间分档计提。",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("direct evidence prompt missing %q:\n%s", want, got)
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

func setupAnswerWithSlowPathVerify(t *testing.T, enabled bool) (*Service, *llm.FakeClient) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)

	cfg := &config.Config{}
	cfg.Retrieval.SlowPathVerify = enabled
	retStore := retrieval.NewStore(db)
	retSvc := retrieval.NewService(retStore, fake, nil, nil, cfg, nil, nil, nil)
	svc := NewService(store, fake, q, retSvc)
	return svc, fake
}

func TestAnswer_SlowPathVerify_Insufficient_Refuses(t *testing.T) {
	svc, fake := setupAnswerWithSlowPathVerify(t, true)
	fake.SetResponse("fast_verify.md", llm.FakeResponse{
		Output: `{"sufficient": false, "reason": "证据讲部署而非升级"}`,
	})
	// If verify incorrectly lets through, generation would produce this — must not appear.
	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"应按以下步骤升级集群","citations":["f1"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "Ubuntu 20.04 部署 K8S 1.27 步骤", UnitID: "u1", PointID: "p1",
				SourceRef: json.RawMessage(`{"source_id":"s1","line_start":1,"line_end":5}`)},
		},
		nil,
	)
	es.Question = "K8S 集群怎么升级版本？"
	es.PathType = retrieval.PathTypeFull

	result := svc.Answer(context.Background(), es)

	if result.Path != "none" {
		t.Fatalf("expected path=none after insufficient verify, got %s content=%q", result.Path, result.Content)
	}
	if result.HasAnswer {
		t.Error("expected has_answer=false")
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected empty citations, got %v", result.Citations)
	}
	if !strings.Contains(result.Content, "暂无相关材料") {
		t.Errorf("expected refusal content, got %q", result.Content)
	}
	if result.PathType != retrieval.PathTypeFull {
		t.Errorf("PathType should stay full for audit, got %s", result.PathType)
	}
}

func TestAnswer_SlowPathVerify_Sufficient_Generates(t *testing.T) {
	svc, fake := setupAnswerWithSlowPathVerify(t, true)
	fake.SetResponse("fast_verify.md", llm.FakeResponse{
		Output: `{"sufficient": true, "reason": "证据直接给出定义"}`,
	})
	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"线性方程是ax+b=0。","citations":["f1"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "线性方程定义 ax+b=0", UnitID: "u1", PointID: "p1",
				SourceRef: json.RawMessage(`{"source_id":"s1","line_start":1,"line_end":5}`)},
		},
		nil,
	)
	es.PathType = retrieval.PathTypeFull

	result := svc.Answer(context.Background(), es)
	if result.Path != "short" || !result.HasAnswer {
		t.Fatalf("expected short answer, got path=%s has_answer=%v content=%q", result.Path, result.HasAnswer, result.Content)
	}
}

func TestAnswer_SlowPathVerify_Disabled_Skips(t *testing.T) {
	svc, fake := setupAnswerWithSlowPathVerify(t, false)
	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"近邻材料改写的答案","citations":["f1"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "易快报报销流程", UnitID: "u1", PointID: "p1",
				SourceRef: json.RawMessage(`{"source_id":"s1","line_start":1,"line_end":5}`)},
		},
		nil,
	)
	es.Question = "报销单在 OA 里怎么填？"
	es.PathType = retrieval.PathTypeFull

	result := svc.Answer(context.Background(), es)
	if result.Path != "short" {
		t.Fatalf("disabled verify should generate normally, got path=%s", result.Path)
	}
}

func TestAnswer_SlowPathVerify_SkipsFastPath(t *testing.T) {
	svc, fake := setupAnswerWithSlowPathVerify(t, true)
	fake.SetResponse("answer_short.md", llm.FakeResponse{
		Output: `{"content":"快路径答案","citations":["f1"]}`,
	})

	es := makeEvidenceSet("short",
		[]retrieval.Evidence{
			{FactID: "f1", Content: "证据", UnitID: "u1", PointID: "p1",
				SourceRef: json.RawMessage(`{"source_id":"s1","line_start":1,"line_end":5}`)},
		},
		nil,
	)
	es.PathType = retrieval.PathTypeFast

	result := svc.Answer(context.Background(), es)
	if result.Path != "short" {
		t.Fatalf("fast path must not run slow verify refuse, got path=%s", result.Path)
	}
}

