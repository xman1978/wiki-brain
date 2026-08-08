package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// TestCompile_LLMCallCountIsFixedAtTwo covers docs/impl/v1/wiki-generation.md
// 完成标准: "LLM 调用次数固定为 2（analyze + compile），不随切面数量变化" —
// the reason the outline/section-generation architecture was withdrawn.
func TestCompile_LLMCallCountIsFixedAtTwo(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)

	_, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 LLM calls (analyze + compile), got %d: %+v", len(calls), calls)
	}
	if calls[0].PromptFile != "wiki_analyze.md" || calls[1].PromptFile != "wiki_compile.md" {
		t.Errorf("expected calls [wiki_analyze.md, wiki_compile.md], got [%s, %s]", calls[0].PromptFile, calls[1].PromptFile)
	}
}

// TestCompile_ConceptKindHintThreadedToPrompts covers docs/impl/v1/kpn.md
// 步骤 3's "类型标注" wiki-side wording nudge: the concept's kind (concept/
// fact) must reach both the analyze and compile prompts as
// {{entry_kind_hint}}, and (docs/impl/v1/wiki.md「概念页 / 事实页」,
// 2026-08-03 修订) also determines page_type — a kind=fact concept compiles
// to page_type="fact", not "concept" (asserted separately by
// TestCompile_HappyPath for the kind=concept case).
func TestCompile_ConceptKindHintThreadedToPrompts(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	if _, err := db.Exec(`UPDATE entries SET kind = 'fact' WHERE entry_id = 'c1'`); err != nil {
		t.Fatal(err)
	}

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeFact})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if page.PageType != PageTypeFact {
		t.Errorf("page_type = %q, want %q", page.PageType, PageTypeFact)
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(calls))
	}
	for _, c := range calls {
		hint := c.Vars["entry_kind_hint"]
		if hint == "" {
			t.Errorf("%s: entry_kind_hint var missing", c.PromptFile)
			continue
		}
		if !strings.Contains(hint, "事实页") {
			t.Errorf("%s: entry_kind_hint = %q, want it to reflect kind=fact", c.PromptFile, hint)
		}
		if label := c.Vars["entry_kind_label"]; label != "事实" {
			t.Errorf("%s: entry_kind_label = %q, want 事实", c.PromptFile, label)
		}
	}
}

// TestCompile_PageTypeDerivedWhenOmitted covers the UI-facing case: the
// manual-compile picker doesn't know a concept's kind ahead of time, so it
// omits page_type entirely (web/index.html's analyzeWikiCandidate/
// compileWikiCandidate calls). Both Analyze and Compile must derive the
// correct page_type from the concept's kind rather than erroring or silently
// defaulting to "concept".
func TestCompile_PageTypeDerivedWhenOmitted(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	if _, err := db.Exec(`UPDATE entries SET kind = 'fact' WHERE entry_id = 'c1'`); err != nil {
		t.Fatal(err)
	}

	ar, err := svc.Analyze(context.Background(), AnalyzeRequest{EntryID: "c1"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if ar.PageType != PageTypeFact {
		t.Errorf("analyze page_type = %q, want %q", ar.PageType, PageTypeFact)
	}

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if page.PageType != PageTypeFact {
		t.Errorf("compile page_type = %q, want %q", page.PageType, PageTypeFact)
	}
}

// TestCompile_PageTypeMustMatchConceptKind covers docs/impl/v1/wiki.md「概念页
// / 事实页」: page_type is not a caller-chosen label, it must match the
// target concept's kind — a concept-kind concept requested as page_type
// "fact" (and vice versa) is rejected before any LLM call.
func TestCompile_PageTypeMustMatchConceptKind(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)

	_, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeFact})
	if err == nil {
		t.Fatal("expected error for page_type=fact against a kind=concept concept, got nil")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls before the page_type/kind mismatch is caught, got %d", len(fake.Calls()))
	}

	_, err = svc.Analyze(context.Background(), AnalyzeRequest{EntryID: "c1", PageType: PageTypeFact})
	if err == nil {
		t.Fatal("expected Analyze to reject page_type=fact against a kind=concept concept, got nil")
	}
}

func TestCompile_HappyPath(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if page.Status != StatusDraft {
		t.Errorf("status = %q, want draft", page.Status)
	}
	if page.Title != "Concept One" {
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
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Every qualifying KP now requires a verified ActivationLink to be
	// selected as compile input at all (docs/design/wiki-compilation.md
	// "反复激活、多次验证、持续采用不是命中次数"), so both p1 and p2's link
	// ids should be recorded.
	var sourceLinkIDs []string
	json.Unmarshal([]byte(page.SourceLinkIDs), &sourceLinkIDs)
	want := map[string]bool{"link-p1": true, "link-p2": true}
	if len(sourceLinkIDs) != 2 {
		t.Fatalf("source_link_ids = %v, want 2 entries (link-p1, link-p2)", sourceLinkIDs)
	}
	for _, id := range sourceLinkIDs {
		if !want[id] {
			t.Errorf("unexpected source_link_id %q", id)
		}
	}
}

// TestCompile_ManualTriggerDropsVerifiedRequirement locks docs/impl/v1/
// wiki.md 步骤 2 "人工指定主题手动编译" (2026-08-07 修订): a manual trigger
// (no result_id) qualifies KPs on lifecycle=current alone — a KP with no
// verified ActivationLink (only a link_candidate row) must still be
// compilable.
func TestCompile_ManualTriggerDropsVerifiedRequirement(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)

	seedEntry(t, db, "c-unverified", "d1", "Unverified Concept")
	seedKU(t, db, "u-unverified", "s1", "c-unverified", "Topic U", 1, 5)
	seedKP(t, db, "p-unverified", "u-unverified", "s1", "unverified point content")
	seedLinkCandidate(t, db, "lc-unverified", "t-u", "p-unverified", 5)
	// Deliberately no seedVerifiedLink for p-unverified.

	fake.SetResponse("wiki_analyze.md", llm.FakeResponse{Output: `{
		"claims": [{"summary": "未验证材料的核心结论", "cited_point_ids": ["p-unverified"], "aspect_id": "misc"}],
		"tensions": []
	}`})
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: "## 摘要\n\n未验证概念。\n\n" +
		"## 稳定结论\n\n未验证材料的核心结论 [p-unverified]\n\n" +
		"## 展开说明\n\n### 核心内容\n\n详细说明。[p-unverified]\n\n" +
		"## 待验证点\n\n暂无。\n\n" +
		"## 依赖来源\n\n见引用。\n"})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c-unverified", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("expected manual compile to succeed without a verified ActivationLink, got: %v", err)
	}
	if page.Status != StatusDraft {
		t.Errorf("expected draft page, got status=%s", page.Status)
	}
}

// TestCompile_StudyTriggerStillRequiresVerified locks the other half of the
// same 2026-08-07 修订: a Study-recommended trigger (result_id present)
// keeps the verified requirement unchanged — the same unverified-only
// concept must still fail to compile.
func TestCompile_StudyTriggerStillRequiresVerified(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	seedEntry(t, db, "c-unverified", "d1", "Unverified Concept")
	seedKU(t, db, "u-unverified", "s1", "c-unverified", "Topic U", 1, 5)
	seedKP(t, db, "p-unverified", "u-unverified", "s1", "unverified point content")
	seedLinkCandidate(t, db, "lc-unverified", "t-u", "p-unverified", 5)

	_, err := svc.Compile(context.Background(), CompileRequest{
		EntryID: "c-unverified", PageType: PageTypeConcept, ResultID: "fake-result-id",
	})
	if err == nil {
		t.Fatal("expected Study-triggered compile to fail without a verified ActivationLink")
	}
}

func TestCompile_DuplicateRejected(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	if _, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept}); err != nil {
		t.Fatalf("first compile: %v", err)
	}
	_, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if !errors.Is(err, ErrPageAlreadyExists) {
		t.Fatalf("err = %v, want ErrPageAlreadyExists", err)
	}
}

// TestCompile_MissingClaimsFailsAfterRetry covers docs/impl/v1/
// wiki-generation.md 3.4: an analyze response with zero usable claims is
// treated as analysis failure, retried once, and fails the compile.
func TestCompile_MissingClaimsFailsAfterRetry(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	fake.SetResponse("wiki_analyze.md", llm.FakeResponse{Output: missingClaimsAnalyzeOutput})

	_, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err == nil {
		t.Fatal("expected compile to fail when analysis produces no usable claims")
	}

	analyzeCalls := 0
	for _, c := range fake.Calls() {
		if c.PromptFile == "wiki_analyze.md" {
			analyzeCalls++
		}
	}
	if analyzeCalls != 2 {
		t.Errorf("expected 2 wiki_analyze.md attempts, got %d", analyzeCalls)
	}

	page, err := svc.store.GetActivePageByEntryID("c1")
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

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
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

	_, err := svc.Compile(context.Background(), CompileRequest{EntryID: "no-such-concept", PageType: PageTypeConcept})
	if err == nil {
		t.Fatal("expected error when concept has no qualifying points")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM call when there are no qualifying points, got %d", len(fake.Calls()))
	}
}

func TestCompile_ResolvesPendingResult(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

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

	if _, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept, ResultID: lr.ResultID}); err != nil {
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
	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
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

	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "point one content", "", "", "", "", 0, 3)
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

	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "point one content", "", "", "", "", 0, 3)
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

	_, ok, _, err := svc.TryDirectAnswer(context.Background(), "point one content", "", "", "", "", 0, 3)
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

	_, ok, _, err := svc.TryDirectAnswer(context.Background(), "point one content", "", "", "", "", 1000, 3)
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
	_, ok, _, err := svc.TryDirectAnswer(context.Background(), "anything", "", "", "", "", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected no hit with an empty wiki index")
	}
}

// TestCompile_PersistsAliasesAndTriggerQuestions covers the post-P0 behavior
// (docs/design/wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组"
// 生成侧 修订): aliases/trigger_questions are no longer LLM output — they're
// a program lookup (subject_synonyms) and a program sample (confident
// traces), so the LLM response no longer needs to (and no longer can)
// supply them.
func TestCompile_PersistsAliasesAndTriggerQuestions(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	seedSubjectSynonym(t, db, "别名一", "Concept One")
	seedSubjectSynonym(t, db, "C1", "Concept One")
	seedConfidentTrace(t, db, "tr1", "这是一个专属触发问法", []string{"p1"})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var aliases, triggers []string
	json.Unmarshal([]byte(page.Aliases), &aliases)
	json.Unmarshal([]byte(page.TriggerQuestions), &triggers)
	wantAliases := map[string]bool{"别名一": true, "C1": true}
	if len(aliases) != 2 || !wantAliases[aliases[0]] || !wantAliases[aliases[1]] {
		t.Errorf("aliases = %v, want [别名一 C1] in some order", aliases)
	}
	if len(triggers) != 1 || triggers[0] != "这是一个专属触发问法" {
		t.Errorf("trigger_questions = %v, want [这是一个专属触发问法]", triggers)
	}
}

func TestCompile_TruncatesAliasesAndTriggerQuestionsAtMax(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	for i := 0; i < 15; i++ {
		alias := fmt.Sprintf("alias%d", i)
		seedSubjectSynonym(t, db, alias, "Concept One")
		seedConfidentTrace(t, db, fmt.Sprintf("tr%d", i), fmt.Sprintf("trigger question %d", i), []string{"p1"})
	}

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
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
	svc, fake, db, _ := setupTestService(t)
	seedConfidentTrace(t, db, "tr-trigger", "这是一个专属触发问法", []string{"p1"})
	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
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
	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "这是一个专属触发问法", "", "", "", "", 0, 3)
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
	page := publishedPage(t, svc, fake) // title == concept name "Concept One"

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"sufficient":true}`})

	// minScore is set unreachably high so no lexical hit could clear it; only
	// the concept entry (question contains the concept name "Concept One",
	// scored independently of Bleve) should surface this page as a candidate.
	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "Concept One 该注意什么", "", "", "", "", 1e9, 3)
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
	publishedPage(t, svc, fake) // c1, title == concept name "Concept One" — first candidate

	// Concept page titles are now the concept name verbatim (no LLM-chosen
	// title — docs/impl/v1/wiki-generation.md 4.1/4.2), so this concept's
	// name itself is crafted to share the "Concept One" phrase with c1's
	// title, giving TryDirectAnswer two lexically-matching candidates.
	seedEntry(t, db, "c2", "d1", "Concept One 相关补充")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedLinkCandidate(t, db, "lc3", "t3", "p3", 10)
	seedVerifiedLink(t, db, "link-p3", "p3")

	fake.SetResponse("wiki_analyze.md", llm.FakeResponse{Output: `{
		"claims": [{"summary": "内容三的核心结论", "cited_point_ids": ["p3"], "aspect_id": "misc"}],
		"tensions": []
	}`})
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: "## 摘要\n\n补充概念的一句话定义。\n\n## 稳定结论\n\n内容三的核心结论 [p3]\n\n## 展开说明\n\n### 核心内容\n\n详细说明三。[p3]\n\n## 待验证点\n\n暂无。\n\n## 依赖来源\n\n见引用。\n"})
	page2, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c2", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile c2: %v", err)
	}
	if _, err := svc.Publish(page2.PageID); err != nil {
		t.Fatalf("publish c2: %v", err)
	}

	// Both pages' titles share "Concept One" (concept-page titles are now the
	// concept name verbatim), so a question built from that phrase lexically
	// hits both, giving TryDirectAnswer two candidates to try in order.
	fake.SetResponseSequence("answer_wiki.md", []llm.FakeResponse{
		{Output: `{"content":"","citations":[],"sufficient":false}`},
		{Output: `{"content":"来自第二个候选页的回答","citations":[],"sufficient":true}`},
	})

	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "Concept One", "", "", "", "", 0, 3)
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

// TestCompile_AggregatesObservedConditions covers docs/design/wiki-compilation.md
// "触发问法取材真实观测，检索匹配复用四元组": a compiled page's
// observed_conditions should be the union of its cited KPs' verified-link
// conditions.
func TestCompile_AggregatesObservedConditions(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	cond1 := activation.NormalizeObservedCondition("database performance tuning", "troubleshoot", "dba", "production", "qterm1", time.Now())
	cond2 := activation.NormalizeObservedCondition("index rebuild strategy", "howto", "dba", "", "qterm2", time.Now())
	setObservedConditions(t, db, "link-p1", []activation.ObservedCondition{cond1})
	setObservedConditions(t, db, "link-p2", []activation.ObservedCondition{cond2})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var conds []activation.ObservedCondition
	if err := json.Unmarshal([]byte(page.ObservedConditions), &conds); err != nil {
		t.Fatalf("unmarshal observed_conditions: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("observed_conditions = %+v, want 2 entries (from link-p1 and link-p2)", conds)
	}
}

// TestCompile_TriggerQuestionsUseRealObservedQuestions covers the same design
// note's generation-side requirement, in its post-P0 form (docs/design/
// wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组" 生成侧
// 修订): trigger_questions is no longer a prompt hint the LLM picks from —
// it's the stored field, filled directly and only from real confirmed
// question text. (Superseded the older version of this test, which asserted
// on a wiki_compile.md prompt var that no longer exists now that the LLM
// isn't asked to produce trigger_questions at all; see
// TestCompile_PersistsAliasesAndTriggerQuestions for the full assertion.)
func TestCompile_TriggerQuestionsUseRealObservedQuestions(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	seedConfidentTrace(t, db, "tr1", "这个真实问法应该出现在生成素材里", []string{"p1"})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var triggers []string
	json.Unmarshal([]byte(page.TriggerQuestions), &triggers)
	found := false
	for _, q := range triggers {
		if q == "这个真实问法应该出现在生成素材里" {
			found = true
		}
	}
	if !found {
		t.Errorf("trigger_questions = %v, want it to contain the seeded confident trace question", triggers)
	}
}

// TestTryDirectAnswer_FourTupleEntry covers docs/impl/v1/wiki.md 步骤 4c: a
// published page whose observed_conditions match the already-parsed
// subject/intent/audience/constraint should surface as a direct-answer
// candidate even when the question text has no lexical overlap with the
// page (title/content/aliases/trigger_questions) and minScore is
// unreachably high — only the four-tuple entry can find it.
func TestTryDirectAnswer_FourTupleEntry(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	activationSvc := newTestActivationSvc(db)
	svc.SetActivationSvc(activationSvc)

	cond := activation.NormalizeObservedCondition("database performance tuning", "troubleshoot", "dba", "production", "qterm1", time.Now())
	setObservedConditions(t, db, "link-p1", []activation.ObservedCondition{cond})

	page := publishedPage(t, svc, fake)

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"sufficient":true}`})

	result, ok, _, err := svc.TryDirectAnswer(context.Background(),
		"完全不重合的措辞，既不在标题里也不在正文里",
		"database performance tuning", "troubleshoot", "dba", "production",
		1e9, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if !ok {
		t.Fatal("expected the four-tuple entry to surface the page despite no lexical overlap and an unreachable min score")
	}
	if result.PageID != page.PageID {
		t.Errorf("page_id = %q, want %q", result.PageID, page.PageID)
	}
}

// TestTryDirectAnswer_FourTupleEmptyQueryNoMatch is the regression test for
// the empty-query guard added to activation.MatchConditionGroups: a page
// whose observed_conditions include a group with empty audience/constraint
// must not match an entirely empty query (subject/intent/audience/constraint
// all ""), which is what the plain POST /answer path (no Session parsing)
// looks like.
func TestTryDirectAnswer_FourTupleEmptyQueryNoMatch(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	activationSvc := newTestActivationSvc(db)
	svc.SetActivationSvc(activationSvc)

	cond := activation.NormalizeObservedCondition("", "", "", "", "qterm1", time.Now())
	setObservedConditions(t, db, "link-p1", []activation.ObservedCondition{cond})

	publishedPage(t, svc, fake)

	_, ok, _, err := svc.TryDirectAnswer(context.Background(),
		"完全不重合的措辞，既不在标题里也不在正文里",
		"", "", "", "",
		1e9, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected an entirely empty query not to match via the four-tuple entry, even against an empty-fields condition group")
	}
}

// TestVerifyClaims_WritesUnsupportedCheck covers docs/impl/v1/
// wiki-generation.md 阶段 E: the support-verdict check is orthogonal to (and
// runs in addition to) the existing citation whitelist — a claim's cited
// point_ids can be entirely in-bounds while the check still flags it
// unsupported. Off by default (config.WikiConfig zero value); this test
// opts in explicitly.
func TestVerifyClaims_WritesUnsupportedCheck(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	svc.cfg.ClaimVerifyEnabled = true
	fake.SetResponse("wiki_claim_verify.md", llm.FakeResponse{Output: `{"results":[
		{"claim_id":"claim-1","verdict":"unsupported","reason":"材料不支持"},
		{"claim_id":"claim-2","verdict":"supported","reason":"符合材料"}
	]}`})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("expected one revision, got %v (err=%v)", revs, err)
	}

	checks, err := svc.store.ListClaimChecks(page.PageID, revs[0].RevisionID)
	if err != nil {
		t.Fatalf("list claim checks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("claim checks = %v, want 2 entries", checks)
	}

	n, err := svc.store.UnsupportedClaimCount(page.PageID, revs[0].RevisionID)
	if err != nil {
		t.Fatalf("unsupported claim count: %v", err)
	}
	if n != 1 {
		t.Errorf("unsupported claim count = %d, want 1", n)
	}
}

// TestVerifyClaims_DisabledByDefaultWritesNoChecks asserts the zero-value
// config (what every other test in this file uses) performs no claim
// verification at all — this is what keeps all the pre-existing Compile
// tests unaffected by this feature's addition.
func TestVerifyClaims_DisabledByDefaultWritesNoChecks(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	revs, _ := svc.store.ListRevisions(page.PageID)
	checks, err := svc.store.ListClaimChecks(page.PageID, revs[0].RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Errorf("claim checks = %v, want none when claim_verify_enabled is off", checks)
	}
}

// compileOutputCitesOnlyP1 — the qualifying set still includes p1/p2, but
// the generated body only ends up citing p1 (as if the model just didn't
// use the rest of its material), so source_point_ids ends up half of the
// qualifying set — exercising the material-usage-rate gate.
const compileOutputCitesOnlyP1 = "## 摘要\n\nEntry One 是这个领域的核心概念。\n\n" +
	"## 稳定结论\n\n内容一的核心结论 [p1]\n\n" +
	"## 展开说明\n\n### 核心内容\n\n详细说明一。[p1]\n\n" +
	"## 待验证点\n\n暂无。\n\n" +
	"## 依赖来源\n\n见引用。\n"

// TestPublish_BlockedByFailedSelfcheckThenForceOverrides covers
// docs/impl/v1/wiki-generation.md 阶段 G: a page that only ends up citing
// half its qualifying material fails the material-usage/uncited-sentence
// axes and can't publish without force=true; force=true publishes anyway
// and flips the stored check to an explicit forced override.
func TestPublish_BlockedByFailedSelfcheckThenForceOverrides(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	svc.cfg.SelfcheckEnabled = true
	fake.SetResponse("wiki_compile.md", llm.FakeResponse{Output: compileOutputCitesOnlyP1})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, err := svc.Publish(page.PageID); !errors.Is(err, ErrQualityGateFailed) {
		t.Fatalf("publish error = %v, want ErrQualityGateFailed", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q after blocked publish, want draft", got.Status)
	}

	published, err := svc.PublishWithForce(context.Background(), page.PageID, true)
	if err != nil {
		t.Fatalf("force publish: %v", err)
	}
	if published.Status != StatusPublished {
		t.Errorf("status = %q after force publish, want published", published.Status)
	}

	revisionID, err := svc.store.LatestRevisionID(page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	qc, err := svc.store.LatestQualityCheck(page.PageID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	if qc == nil || !qc.Passed || !qc.Forced {
		t.Errorf("quality check = %+v, want passed=true forced=true after override", qc)
	}
}

// TestSelfcheck_CachesPerRevision asserts a second Selfcheck call for the
// same unchanged revision returns the cached row instead of replaying
// answer_wiki.md calls again (docs/impl/v1/wiki-generation.md 阶段 G "与
// publish 的关系": "同一 revision 重复 publish 不重跑回放").
func TestSelfcheck_CachesPerRevision(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	svc.cfg.SelfcheckEnabled = true
	seedConfidentTrace(t, db, "tr1", "问题一", []string{"p1"})
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答","citations":["p1"],"sufficient":true}`})

	page, err := svc.Compile(context.Background(), CompileRequest{EntryID: "c1", PageType: PageTypeConcept})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, err := svc.Selfcheck(context.Background(), page.PageID); err != nil {
		t.Fatalf("selfcheck: %v", err)
	}
	callsAfterFirst := len(fake.Calls())

	if _, err := svc.Selfcheck(context.Background(), page.PageID); err != nil {
		t.Fatalf("second selfcheck: %v", err)
	}
	if len(fake.Calls()) != callsAfterFirst {
		t.Errorf("second selfcheck made %d new llm calls, want 0 (cached)", len(fake.Calls())-callsAfterFirst)
	}
}
