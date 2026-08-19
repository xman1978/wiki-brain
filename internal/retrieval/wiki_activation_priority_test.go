package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

// setupTestServiceWithWikiAndActivation wires both a real wiki.Service (one
// published page, same shape as setupTestServiceWithWiki in wiki_test.go)
// and a real activation.Service (same shape as
// setupTestServiceWithActivation in fastpath_test.go) onto a single Service
// instance sharing one DB/store, so both 第 0 层 (Wiki 直答) and 第 1 层
// (ActivationLink Match) can be exercised together through
// RetrieveWithProgress — needed to test the 2026-08-19 parallel-dispatch
// priority rule (docs/impl/v1/retrieval.md「检索总流程」编注 and
// service.go RetrieveWithProgress): when both layers hit on the same
// question, ActivationLink must win.
func setupTestServiceWithWikiAndActivation(t *testing.T) (*Service, *llm.FakeClient, *activation.Service, *wiki.Service) {
	t.Helper()
	svc, fake, store := setupTestService(t)

	// Wiki side (mirrors wiki_test.go's setupTestServiceWithWiki).
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

	// Activation side (mirrors fastpath_test.go's setupTestServiceWithActivation).
	activationStore := activation.NewStore(store.db)
	matcher := activation.NewMatcher(activationStore)
	activationSvc := activation.NewService(activationStore, matcher)
	svc.activationSvc = activationSvc
	svc.cfg.Retrieval.FastPath = true
	svc.cfg.Retrieval.FastPathFallback = true

	return svc, fake, activationSvc, wikiSvc
}

// TestRetrieveWithProgress_BothWikiAndActivationHit_ActivationWins covers
// the 2026-08-19 parallel-dispatch priority rule: when a question hits both
// a published Wiki page (sufficient=true) and an ActivationLink, the final
// result must come from the ActivationLink fast path (path_type=fast), not
// the Wiki direct answer — even though both layers run concurrently and the
// Wiki LLM call still fires (and is discarded).
func TestRetrieveWithProgress_BothWikiAndActivationHit_ActivationWins(t *testing.T) {
	svc, fake, activationSvc, _ := setupTestServiceWithWikiAndActivation(t)

	// Must match both the Wiki page lexically (title "Linear Equations
	// Wiki") and the seeded ActivationLink's question_terms fallback match,
	// so both layers genuinely hit on the same question.
	question := "linear equations"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")

	// Wiki side would also be sufficient if it were used.
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答：ax+b=0 是线性方程 [p1]","citations":["p1"],"sufficient":true}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "kpn neighbor"}, {"candidate_id": "c2", "relevant": true, "analysis": "kpn neighbor"}]}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast (ActivationLink must win over a simultaneous Wiki hit)", es.PathType)
	}
	if es.WikiPageID != "" {
		t.Errorf("expected no wiki_page_id on the winning result, got %q", es.WikiPageID)
	}
	if len(es.ActivationHits) != 1 || es.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected 1 activation hit for p1, got %+v", es.ActivationHits)
	}

	// The Wiki layer still ran concurrently (and was discarded) — confirms
	// this is genuinely parallel dispatch, not a short-circuit that skips
	// Wiki once ActivationLink is known to be viable.
	wikiCalled := false
	for _, c := range fake.Calls() {
		if c.PromptFile == "answer_wiki.md" {
			wikiCalled = true
		}
	}
	if !wikiCalled {
		t.Error("expected answer_wiki.md to have been called (parallel dispatch), even though its result was discarded")
	}
}

// TestRetrieveWithProgress_OnlyWikiHits_ActivationMisses_UsesWiki covers the
// other branch of the priority rule: when ActivationLink does not hit at
// all, a simultaneous Wiki hit must still be used.
func TestRetrieveWithProgress_OnlyWikiHits_ActivationMisses_UsesWiki(t *testing.T) {
	svc, fake, _, _ := setupTestServiceWithWikiAndActivation(t)

	question := "linear equations"
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答：ax+b=0 是线性方程 [p1]","citations":["p1"],"sufficient":true}`})

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeWiki {
		t.Fatalf("path_type = %q, want wiki (no ActivationLink seeded, so Wiki hit should be used)", es.PathType)
	}
	if es.WikiPageID != "w1" {
		t.Errorf("wiki_page_id = %q, want w1", es.WikiPageID)
	}
}
