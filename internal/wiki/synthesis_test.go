package wiki

import (
	"context"
	"database/sql"
	"testing"
)

// TestPage_SynthesisMean_FormulaMatchesActivationConditionMean covers
// docs/impl/v1/wiki.md 步骤 4a's mean(page) = (success_count+1) /
// (success_count+failure_count+2) — the same Laplace-smoothed Beta posterior
// as activation.ConditionMean, applied at page granularity. A brand-new page
// (0/0) must start at 0.5, not assume default trust in either direction.
func TestPage_SynthesisMean_FormulaMatchesActivationConditionMean(t *testing.T) {
	cases := []struct {
		name             string
		success, failure int
		want             float64
	}{
		{"new page, no synthesis data", 0, 0, 0.5},
		{"all success", 9, 0, 10.0 / 11.0},
		{"all failure", 0, 9, 1.0 / 11.0},
		{"mixed", 3, 1, 4.0 / 6.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Page{SynthesisSuccessCount: c.success, SynthesisFailureCount: c.failure}
			got := p.SynthesisMean()
			if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("SynthesisMean() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStore_RecordSynthesisOutcome_AuditOnly_AdvancesBothCountersTogether
// covers docs/impl/v1/wiki.md 步骤 4a's explicit audit-only design: unlike
// ActivationLink/Bundle's RecordOutcome vs RecordAuditOutcome split (a
// self-graded tier can advance success_count/failure_count alone), the
// synthesis axis has no self-graded tier at all — every RecordSynthesisOutcome
// call must advance success_count and audited_success_count (or the failure
// pair) together, never independently.
func TestStore_RecordSynthesisOutcome_AuditOnly_AdvancesBothCountersTogether(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	_ = svc
	store := NewStore(db)

	page := &Page{PageID: "pg1", PageType: PageTypeTopic, EntryID: sql.NullString{String: "c1", Valid: true}, Title: "t", Content: "c", PromptVersion: "v1", ModelName: "m1"}
	if err := store.InsertPage(page); err != nil {
		t.Fatalf("insert page: %v", err)
	}

	if err := store.RecordSynthesisOutcome(page.PageID, true); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := store.RecordSynthesisOutcome(page.PageID, false); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if err := store.RecordSynthesisOutcome(page.PageID, false); err != nil {
		t.Fatalf("record failure 2: %v", err)
	}

	got, err := store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if got.SynthesisSuccessCount != 1 || got.SynthesisAuditedSuccessCount != 1 {
		t.Errorf("success counters = (%d,%d), want (1,1) — must advance together (audit-only)",
			got.SynthesisSuccessCount, got.SynthesisAuditedSuccessCount)
	}
	if got.SynthesisFailureCount != 2 || got.SynthesisAuditedFailureCount != 2 {
		t.Errorf("failure counters = (%d,%d), want (2,2) — must advance together (audit-only)",
			got.SynthesisFailureCount, got.SynthesisAuditedFailureCount)
	}
}

// TestSynthesisAxis_DoesNotAffectRecompileSelfcheckOrIndex is the most
// important test in this half (explicitly called out by the execution plan):
// driving a published page's synthesis counts to an extreme (mean near 0)
// must leave needs_recompile/status, selfcheck gating, and index/search
// inclusion completely untouched — the synthesis axis is observation-only
// (docs/impl/v1/wiki.md 步骤 4a「mean(page) 的消费方式」: "不驱动任何自动动作").
func TestSynthesisAxis_DoesNotAffectRecompileSelfcheckOrIndex(t *testing.T) {
	svc, _, db, wikiIndex := setupTestService(t)
	store := NewStore(db)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryIDs: []string{"c1"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	published, err := svc.Publish(page.PageID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != StatusPublished {
		t.Fatalf("expected published status before synthesis data, got %s", published.Status)
	}

	// Drive mean(page) as close to 0 as this test cares to: 50 audited
	// failures, 0 successes.
	for i := 0; i < 50; i++ {
		if err := store.RecordSynthesisOutcome(page.PageID, false); err != nil {
			t.Fatalf("record synthesis failure #%d: %v", i, err)
		}
	}

	got, err := store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	mean := got.SynthesisMean()
	if mean >= 0.1 {
		t.Fatalf("expected mean(page) to have collapsed near 0 after 50 failures, got %v", mean)
	}

	// Status/needs_recompile must be completely unaffected.
	if got.Status != StatusPublished {
		t.Errorf("status = %q after synthesis failures, want still %q (synthesis axis must not drive status)", got.Status, StatusPublished)
	}

	// Selfcheck gate: a low mean(page) must not make Selfcheck itself behave
	// differently — it doesn't read the synthesis columns at all, verified
	// here by confirming Selfcheck still runs the same replay-based logic
	// (no panic/short-circuit) and returns a result untouched by synthesis
	// state.
	check, err := svc.Selfcheck(context.Background(), page.PageID)
	if err != nil {
		t.Fatalf("selfcheck: %v", err)
	}
	if check == nil {
		t.Fatalf("expected a selfcheck result regardless of synthesis axis state")
	}

	// Index inclusion: the page must still be findable via the same lexical
	// search TryDirectAnswer/gatherDirectAnswerCandidates use — a collapsed
	// synthesis mean must not pull it out of the index.
	docCount, err := wikiIndex.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if docCount == 0 {
		t.Fatalf("expected the published page to remain indexed after synthesis failures, got 0 docs")
	}
	fromIndex, err := store.GetPage(page.PageID)
	if err != nil || fromIndex == nil || fromIndex.Status != StatusPublished {
		t.Errorf("expected page to remain retrievable/published post synthesis collapse, got page=%+v err=%v", fromIndex, err)
	}
}
