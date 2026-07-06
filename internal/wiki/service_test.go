package wiki

import (
	"context"
	"encoding/json"
	"errors"
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

	result, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0)
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

	result, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0)
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

	_, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 0)
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

	_, ok, err := svc.TryDirectAnswer(context.Background(), "point one content", 1000)
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
	_, ok, err := svc.TryDirectAnswer(context.Background(), "anything", 0)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	if ok {
		t.Fatal("expected no hit with an empty wiki index")
	}
}
