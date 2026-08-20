package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

// setupTestServiceWithWiki reuses setupTestService's fixture and wires a real
// wiki.Service with one published page directly indexed (bypassing Compile),
// so docs/impl/v1/retrieval.md 第 0 层 can be exercised end-to-end through
// RetrieveWithProgress without needing the full Study→compile→publish chain.
func setupTestServiceWithWiki(t *testing.T) (*Service, *llm.FakeClient, *wiki.Service) {
	t.Helper()
	svc, fake, store := setupTestService(t)

	wikiStore := wiki.NewStore(store.db)
	page := &wiki.Page{
		PageID:         "w1",
		PageType:       wiki.PageTypeConcept,
		Title:          "Linear Equations Wiki",
		Content:        "## 稳定结论\n[p1] ax+b=0 is linear\n\n## 展开说明\n...\n\n## 待验证点\n...\n\n## 依赖来源\n...\n",
		Status:         wiki.StatusDraft,
		SourcePointIDs: `["p1"]`,
		SourceUnitIDs:  `["u1"]`,
		CompiledFrom:   `[]`,
		PromptVersion:  "v1",
		ModelName:      "reasoning",
	}
	if err := wikiStore.InsertPage(page); err != nil {
		t.Fatal(err)
	}

	idxMgr, err := index.NewManager(foundation.NewTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	wikiCfg := config.WikiConfig{CompileMaxChars: 12000, RecompileNewKPMin: 2}
	wikiSvc := wiki.NewService(wikiStore, fake, idxMgr.Wiki, idxMgr.Points, idxMgr.Outlines, wikiCfg)
	if _, err := wikiSvc.Publish(page.PageID); err != nil {
		t.Fatal(err)
	}

	svc.wikiSvc = wikiSvc
	svc.cfg.Retrieval.WikiMinScore = 0
	return svc, fake, wikiSvc
}

func TestRetrieveWithProgress_WikiHit(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答：ax+b=0 是线性方程 [p1]","citations":["p1"],"coverage":"full"}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: "linear equations"}, nil)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if es.PathType != PathTypeWiki {
		t.Fatalf("path_type = %q, want wiki", es.PathType)
	}
	if es.WikiPageID != "w1" {
		t.Errorf("wiki_page_id = %q, want w1", es.WikiPageID)
	}
	if len(es.CitedPointIDs) != 1 || es.CitedPointIDs[0] != "p1" {
		t.Errorf("cited_point_ids = %v, want [p1]", es.CitedPointIDs)
	}
	if es.WikiAnswerContent == "" {
		t.Error("expected WikiAnswerContent to be populated")
	}
	// 2026-08-20 改判: 之前这里断言 DirectEvidence/Supporting 恒为空——那正是
	// 缺陷本身（点击"证据X"打开的抽屉靠 DirectEvidence 渲染，永远为空导致
	// 抽屉空白）。buildWikiEvidence 把 CitedPointIDs 解析成可展示的 Evidence，
	// FactID 就是 point_id 本身（跟答案正文里内联的 [point_id] 对上）。
	if len(es.DirectEvidence) != 1 {
		t.Fatalf("expected wiki path to resolve its cited point into displayable evidence, got %d direct", len(es.DirectEvidence))
	}
	if es.DirectEvidence[0].FactID != "p1" || es.DirectEvidence[0].PointID != "p1" {
		t.Errorf("wiki evidence fact_id/point_id = %q/%q, want p1/p1 (fact_id must match the inline [point_id] citation)", es.DirectEvidence[0].FactID, es.DirectEvidence[0].PointID)
	}
	if len(es.Supporting) != 0 {
		t.Errorf("expected wiki path to carry no supporting evidence, got %d", len(es.Supporting))
	}
}

func TestRetrieveWithProgress_WikiInsufficientFallsThrough(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"","citations":[],"coverage":"none"}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: "linear equations"}, nil)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if es.PathType == PathTypeWiki {
		t.Fatalf("expected fall-through to fast/slow path when wiki answer is insufficient, got path_type=wiki")
	}
}

func TestRetrieveWithProgress_ForceFullSkipsWiki(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"coverage":"full"}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: "linear equations", ForceFull: true}, nil)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if es.PathType == PathTypeWiki {
		t.Fatalf("expected force_full to skip the wiki layer entirely")
	}
	for _, c := range fake.Calls() {
		if c.PromptFile == "answer_wiki.md" {
			t.Errorf("expected no answer_wiki.md call when force_full=true")
		}
	}
}
