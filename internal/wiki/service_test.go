package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestCompile_HappyPath(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})

	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if page.Status != StatusDraft {
		t.Errorf("status = %q, want draft", page.Status)
	}
	if page.Title != "Concept One 知识页" {
		t.Errorf("title = %q", page.Title)
	}
	if !hasRequiredSections(page.Content) {
		t.Errorf("content missing required sections: %q", page.Content)
	}

	var sourcePointIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	if len(sourcePointIDs) != 2 {
		t.Errorf("source_point_ids = %v, want 2 entries", sourcePointIDs)
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil || len(revs) != 1 || revs[0].Reason != "compile" {
		t.Errorf("expected one compile revision, got %v (err=%v)", revs, err)
	}
}

func TestCompile_RecordsVerifiedLinkIDs(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})
	seedVerifiedLink(t, db, "link-p1", "p1")

	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var sourceLinkIDs []string
	json.Unmarshal([]byte(page.SourceLinkIDs), &sourceLinkIDs)
	if len(sourceLinkIDs) != 1 || sourceLinkIDs[0] != "link-p1" {
		t.Errorf("source_link_ids = %v, want [link-p1] (p2 has no link, p1's is verified)", sourceLinkIDs)
	}
}

func TestCompile_DuplicateRejected(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})

	if _, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept}); err != nil {
		t.Fatalf("first compile: %v", err)
	}
	_, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if !errors.Is(err, ErrPageAlreadyExists) {
		t.Fatalf("err = %v, want ErrPageAlreadyExists", err)
	}
}

func TestCompile_MissingSectionsFailsAfterRetry(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: missingSectionsCompileOutput})

	_, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err == nil {
		t.Fatal("expected compile to fail when required sections are missing")
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Errorf("expected 2 LLM attempts, got %d", len(calls))
	}

	page, err := svc.store.GetActivePageByConceptID("c1")
	if err != nil {
		t.Fatal(err)
	}
	if page != nil {
		t.Errorf("expected no page to be persisted after compile failure, got %+v", page)
	}
}

func TestCompile_HallucinatedCitationsStripped(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: hallucinatedCiteCompileOutput})

	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if strings.Contains(page.Content, "[p999]") {
		t.Errorf("expected hallucinated tag [p999] to be stripped from content: %q", page.Content)
	}

	var sourcePointIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	for _, id := range sourcePointIDs {
		if id == "p999" {
			t.Errorf("expected p999 not to appear in source_point_ids: %v", sourcePointIDs)
		}
	}
}

func TestCompile_NoQualifyingPoints(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})

	_, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "no-such-concept", PageType: PageTypeConcept})
	if err == nil {
		t.Fatal("expected error when concept has no qualifying points")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM call when there are no qualifying points, got %d", len(fake.Calls()))
	}
}

func TestCompile_ResolvesPendingResult(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})

	activationSvc := newTestActivationSvc(db)
	svc.SetActivationSvc(activationSvc)

	lr := &activation.LearningResult{
		Action:     activation.ActionWikiCandidate,
		ObjectType: activation.ObjectTypeWikiPage,
		ObjectID:   "c1",
		Reason:     "ready",
		Status:     activation.ResultPendingConfirm,
	}
	if err := activationSvc.Store().InsertLearningResult(lr); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept, ResultID: lr.ResultID}); err != nil {
		t.Fatalf("compile: %v", err)
	}

	results, err := activationSvc.Store().ListLearningResultsByObject(activation.ObjectTypeWikiPage, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != activation.ResultApplied {
		t.Errorf("expected pending result to be resolved to applied, got %+v", results)
	}
}

func publishedPage(t *testing.T, svc *Service, fake *llm.FakeClient) *Page {
	t.Helper()
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: validCompileOutput})
	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	page, err = svc.Publish(page.PageID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return page
}

func TestPublish_IndexesPage(t *testing.T) {
	svc, fake, _, idx := setupTestService(t)
	page := publishedPage(t, svc, fake)

	if page.Status != StatusPublished {
		t.Errorf("status = %q, want published", page.Status)
	}
	doc, err := idx.Document(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil {
		t.Errorf("expected page %s to be indexed after publish", page.PageID)
	}
}

func TestPublish_InvalidFromArchived(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	page := publishedPage(t, svc, fake)

	if _, err := svc.Archive(page.PageID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	_, err := svc.Publish(page.PageID)
	if !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("err = %v, want ErrInvalidStateTransition", err)
	}
}

func TestArchive_Terminal(t *testing.T) {
	svc, fake, _, idx := setupTestService(t)
	page := publishedPage(t, svc, fake)

	if _, err := svc.Archive(page.PageID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusArchived {
		t.Errorf("status = %q, want archived", got.Status)
	}
	doc, _ := idx.Document(page.PageID)
	if doc != nil {
		t.Errorf("expected archived page to be removed from index")
	}

	if _, err := svc.Recompile(context.Background(), page.PageID, "manual", nil); !errors.Is(err, ErrPageArchived) {
		t.Errorf("recompile on archived page: err = %v, want ErrPageArchived", err)
	}
}

func TestRecompile_NewRevisionThenRepublish(t *testing.T) {
	svc, fake, _, idx := setupTestService(t)
	page := publishedPage(t, svc, fake)

	page, err := svc.Recompile(context.Background(), page.PageID, "manual_recompile", nil)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if page.Status != StatusDraft {
		t.Errorf("status after recompile = %q, want draft", page.Status)
	}
	doc, _ := idx.Document(page.PageID)
	if doc != nil {
		t.Errorf("expected page to be removed from index while awaiting republish")
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || revs[1].Reason != "manual_recompile" {
		t.Errorf("expected 2 revisions (compile, manual_recompile), got %+v", revs)
	}

	page, err = svc.Publish(page.PageID)
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if page.Status != StatusPublished {
		t.Errorf("status = %q, want published", page.Status)
	}
}

func TestNotifyPointsLifecycleChanged_MarksNeedsRecompile(t *testing.T) {
	svc, fake, _, idx := setupTestService(t)
	page := publishedPage(t, svc, fake)

	if err := svc.NotifyPointsLifecycleChanged([]string{"p1"}); err != nil {
		t.Fatalf("notify: %v", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsRecompile {
		t.Errorf("status = %q, want needs_recompile", got.Status)
	}
	doc, _ := idx.Document(page.PageID)
	if doc != nil {
		t.Errorf("expected page to be removed from index after lifecycle change")
	}
}

func TestNotifyPointsLifecycleChanged_UnaffectedPageUntouched(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	page := publishedPage(t, svc, fake)

	if err := svc.NotifyPointsLifecycleChanged([]string{"some-other-point"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPublished {
		t.Errorf("status = %q, want unchanged published", got.Status)
	}
}

func TestScanForNewQualifyingKP_FlagsAboveThreshold(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	page := publishedPage(t, svc, fake) // source_point_ids has 2 entries (p1, p2)

	flagged, err := svc.ScanForNewQualifyingKP(map[string]int{"c1": 4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(flagged) != 1 || flagged[0].PageID != page.PageID {
		t.Fatalf("expected page %s flagged, got %+v", page.PageID, flagged)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsRecompile {
		t.Errorf("status = %q, want needs_recompile", got.Status)
	}
}

func TestScanForNewQualifyingKP_BelowThresholdNotFlagged(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	page := publishedPage(t, svc, fake) // 2 source_point_ids

	flagged, err := svc.ScanForNewQualifyingKP(map[string]int{"c1": 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(flagged) != 0 {
		t.Errorf("expected no pages flagged, got %+v", flagged)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPublished {
		t.Errorf("status = %q, want still published", got.Status)
	}
}

func TestTryDirectAnswer_Sufficient(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	publishedPage(t, svc, fake)

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答内容 [p1]","citations":["p1"],"sufficient":true}`})

	result, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected sufficient direct answer")
	}
	if len(result.CitedPointIDs) != 1 || result.CitedPointIDs[0] != "p1" {
		t.Errorf("cited_point_ids = %v, want [p1]", result.CitedPointIDs)
	}
	if result.Content != "回答内容 [p1]" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestTryDirectAnswer_HallucinatedCitationFiltered(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	publishedPage(t, svc, fake)

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答内容","citations":["p1","p999"],"sufficient":true}`})

	result, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected sufficient direct answer")
	}
	for _, id := range result.CitedPointIDs {
		if id == "p999" {
			t.Errorf("expected hallucinated citation p999 to be filtered, got %v", result.CitedPointIDs)
		}
	}
}

func TestTryDirectAnswer_InsufficientFallsBack(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	publishedPage(t, svc, fake)

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"","citations":[],"sufficient":false}`})

	_, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected fallback (ok=false) when page reports insufficient")
	}
}

func TestTryDirectAnswer_NoHitBelowMinScoreSkipsLLM(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	publishedPage(t, svc, fake)

	_, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 1000, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected no hit when min score is unreachably high")
	}
	for _, c := range fake.Calls() {
		if c.PromptFile == "answer_wiki.md" {
			t.Errorf("expected no answer_wiki.md LLM call when the score bar isn't cleared")
		}
	}
}

func TestTryDirectAnswer_NoPublishedPagesNoHit(t *testing.T) {
	svc, _, _, _ := setupTestService(t)
	_, ok, err := svc.TryDirectAnswer(context.Background(), "anything", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected no hit with an empty wiki index")
	}
}

const compileOutputWithTriggers = `{
	"title": "Concept One 知识页",
	"content": "## 稳定结论\n[p1] 内容一\n\n## 展开说明\n详细说明。\n\n## 待验证点\n暂无。\n\n## 依赖来源\n见引用。\n",
	"cited_point_ids": ["p1"],
	"aliases": ["别名一", "C1"],
	"trigger_questions": ["这是一个专属触发问法"]
}`

func TestCompile_PersistsAliasesAndTriggerQuestions(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: compileOutputWithTriggers})

	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var aliases, triggers []string
	json.Unmarshal([]byte(page.Aliases), &aliases)
	json.Unmarshal([]byte(page.TriggerQuestions), &triggers)
	if len(aliases) != 2 || aliases[0] != "别名一" {
		t.Errorf("aliases = %v, want [别名一 C1]", aliases)
	}
	if len(triggers) != 1 || triggers[0] != "这是一个专属触发问法" {
		t.Errorf("trigger_questions = %v, want [这是一个专属触发问法]", triggers)
	}
}

func TestCompile_TruncatesAliasesAndTriggerQuestionsAtMax(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)

	var aliases, triggers []string
	for i := 0; i < 15; i++ {
		aliases = append(aliases, fmt.Sprintf("alias%d", i))
		triggers = append(triggers, fmt.Sprintf("trigger%d", i))
	}
	out, err := json.Marshal(map[string]interface{}{
		"title":             "Concept One 知识页",
		"content":           "## 稳定结论\n[p1] 内容一\n\n## 展开说明\n详细说明。\n\n## 待验证点\n暂无。\n\n## 依赖来源\n见引用。\n",
		"cited_point_ids":   []string{"p1"},
		"aliases":           aliases,
		"trigger_questions": triggers,
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: string(out)})

	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var gotAliases, gotTriggers []string
	json.Unmarshal([]byte(page.Aliases), &gotAliases)
	json.Unmarshal([]byte(page.TriggerQuestions), &gotTriggers)
	if len(gotAliases) != 10 {
		t.Errorf("aliases len = %d, want 10 (default trigger_questions_max)", len(gotAliases))
	}
	if len(gotTriggers) != 10 {
		t.Errorf("trigger_questions len = %d, want 10 (default trigger_questions_max)", len(gotTriggers))
	}
}

func TestTryDirectAnswer_TriggerQuestionRoutesWithoutContentOverlap(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: compileOutputWithTriggers})
	page, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := svc.Publish(page.PageID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"sufficient":true}`})

	// The question exactly echoes the compiled trigger_questions entry, which
	// shares no vocabulary with the page's title/content — only the trigger
	// index field can produce a lexical hit here.
	result, ok, err := svc.TryDirectAnswer(context.Background(), "这是一个专属触发问法", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected trigger_questions to route this question to the page")
	}
	if result.PageID != page.PageID {
		t.Errorf("page_id = %q, want %q", result.PageID, page.PageID)
	}
}

func TestTryDirectAnswer_ConceptEntryBypassesMinScore(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	page := publishedPage(t, svc, fake) // title "Concept One 知识页", concept name "Concept One"

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"sufficient":true}`})

	// minScore is set unreachably high so no lexical hit could clear it; only
	// the concept entry (question contains the concept name "Concept One",
	// scored independently of Bleve) should surface this page as a candidate.
	result, ok, err := svc.TryDirectAnswer(context.Background(), "Concept One 该注意什么", 1e9, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected concept entry to surface the page despite the unreachable min score")
	}
	if result.PageID != page.PageID {
		t.Errorf("page_id = %q, want %q", result.PageID, page.PageID)
	}
}

func TestTryDirectAnswer_TopNRetriesNextCandidateOnInsufficient(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	publishedPage(t, svc, fake) // c1, title "Concept One 知识页" — first candidate

	seedConcept(t, db, "c2", "d1", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedLinkCandidate(t, db, "lc3", "t3", "p3", 10)

	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: `{
		"title": "Concept One 知识页 相关补充",
		"content": "## 稳定结论\n[p3] 内容三\n\n## 展开说明\n详细说明。\n\n## 待验证点\n暂无。\n\n## 依赖来源\n见引用。\n",
		"cited_point_ids": ["p3"]
	}`})
	page2, err := svc.Compile(context.Background(), CompileRequest{ConceptID: "c2", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile c2: %v", err)
	}
	if _, err := svc.Publish(page2.PageID); err != nil {
		t.Fatalf("publish c2: %v", err)
	}

	// Both pages' titles share "Concept One 知识页", so a question built from
	// that phrase lexically hits both, giving TryDirectAnswer two candidates
	// to try in order.
	fake.SetResponseSequence("answer_wiki.md", []llm.FakeResponse{
		{Output: `{"content":"","citations":[],"sufficient":false}`},
		{Output: `{"content":"来自第二个候选页的回答","citations":[],"sufficient":true}`},
	})

	result, ok, err := svc.TryDirectAnswer(context.Background(), "Concept One 知识页", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected the second candidate to answer sufficiently after the first declined")
	}
	if result.Content != "来自第二个候选页的回答" {
		t.Errorf("content = %q, want the second candidate's answer", result.Content)
	}

	var answerCalls int
	for _, c := range fake.Calls() {
		if c.PromptFile == "answer_wiki.md" {
			answerCalls++
		}
	}
	if answerCalls != 2 {
		t.Errorf("answer_wiki.md calls = %d, want 2 (first declined, second retried)", answerCalls)
	}
}
