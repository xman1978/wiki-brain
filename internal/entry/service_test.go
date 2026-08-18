package entry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupService(t *testing.T) (*Service, *Store, *sql.DB) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig(), nil)
	return svc, store, db
}

// --- Add-cluster scan ---

func TestScanAddClusters_AllThresholdsMet_CreatesCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "并发编程 基础", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedKU(t, db, "u2", "s1", "并发编程 进阶", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s1")

	pointIDs := []string{"p1", "p2"}
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h2", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h3", pointIDs, "entry_gap")

	summary := svc.Scan()
	if summary.AddCreated != 1 {
		t.Fatalf("expected 1 add candidate created, got %d (summary=%+v)", summary.AddCreated, summary)
	}

	candidates, err := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("expected 1 pending add candidate, got %d (err=%v)", len(candidates), err)
	}
	c := candidates[0]
	if c.SuggestedName.String == "" {
		t.Error("expected non-empty suggested_name")
	}
	if !c.DomainID.Valid || c.DomainID.String != "d1" {
		t.Errorf("expected domain_id=d1, got %+v", c.DomainID)
	}
	gotPoints := candidatePointIDs(t, &c)
	if len(gotPoints) != 2 {
		t.Errorf("expected 2 point_ids, got %v", gotPoints)
	}
	ev := candidateEvidence(t, &c)
	if int(ev["event_count"].(float64)) != 3 {
		t.Errorf("evidence event_count = %v, want 3", ev["event_count"])
	}
	if int(ev["distinct_question_count"].(float64)) != 3 {
		t.Errorf("evidence distinct_question_count = %v, want 3", ev["distinct_question_count"])
	}
}

func TestScanAddClusters_EventCountNotMet_NoCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	pointIDs := []string{"p1"}
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h2", pointIDs, "entry_gap")
	// only 2 events, AddEventMin=3

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate (event count below threshold), got %d", len(candidates))
	}
}

func TestScanAddClusters_DistinctCountNotMet_NoCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	pointIDs := []string{"p1"}
	// 3 events but all from the same question_hash -> distinct=1 < AddDistinctMin=2
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate (distinct question count below threshold), got %d", len(candidates))
	}
}

func TestScanAddClusters_OverlapNotMet_EventsSplitAcrossClusters(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	for _, id := range []string{"p1", "p2", "p3"} {
		seedKU(t, db, "u-"+id, "s1", "topic "+id, sql.NullString{})
		seedKP(t, db, id, "u-"+id, "s1")
	}

	// Three events with pairwise-disjoint point sets: each starts its own
	// cluster (Jaccard=0 < 0.5), so no cluster reaches AddEventMin=3.
	seedEntryGapEvent(t, db, newEventID(), "h1", []string{"p1"}, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h2", []string{"p2"}, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h3", []string{"p3"}, "entry_gap")

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate (no cluster reaches event count), got %d", len(candidates))
	}
}

func TestScanAddClusters_LinkGapEventsIgnored(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	pointIDs := []string{"p1"}
	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "link_gap")
	seedEntryGapEvent(t, db, newEventID(), "h2", pointIDs, "link_gap")
	seedEntryGapEvent(t, db, newEventID(), "h3", pointIDs, "link_gap")

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate from link_gap events, got %d", len(candidates))
	}
}

func TestScanAddClusters_SecondCycleUpdatesExisting_NoDuplicateRow(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "并发 基础", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedKU(t, db, "u2", "s1", "并发 进阶", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s1")
	pointIDs := []string{"p1", "p2"}

	seedEntryGapEvent(t, db, newEventID(), "h1", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h2", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h3", pointIDs, "entry_gap")
	summary1 := svc.Scan()
	if summary1.AddCreated != 1 {
		t.Fatalf("expected 1 candidate created in cycle 1, got %d", summary1.AddCreated)
	}

	before, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	beforeEventCount := len(candidateEventIDs(t, &before[0]))

	// Cycle 2: two more overlapping events arrive.
	seedEntryGapEvent(t, db, newEventID(), "h4", pointIDs, "entry_gap")
	seedEntryGapEvent(t, db, newEventID(), "h5", pointIDs, "entry_gap")
	summary2 := svc.Scan()
	if summary2.AddCreated != 0 {
		t.Errorf("expected 0 new candidates in cycle 2, got %d", summary2.AddCreated)
	}
	if summary2.AddUpdated != 1 {
		t.Errorf("expected 1 candidate updated in cycle 2, got %d", summary2.AddUpdated)
	}

	after, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if len(after) != 1 {
		t.Fatalf("expected exactly 1 pending candidate after 2 cycles, got %d", len(after))
	}
	afterEventCount := len(candidateEventIDs(t, &after[0]))
	if afterEventCount != beforeEventCount+2 {
		t.Errorf("expected event_ids to grow by 2, got %d -> %d", beforeEventCount, afterEventCount)
	}
}

// --- Merge candidate scan ---

func seedMergeScenario(t *testing.T, db *sql.DB, conceptA, conceptB string, mergedInto sql.NullString) {
	t.Helper()
	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, conceptA, "d1", sql.NullString{})
	seedEntry(t, db, conceptB, "d1", mergedInto)
	seedKU(t, db, "u-a", "s1", "concept a topic", sql.NullString{String: conceptA, Valid: true})
	seedKP(t, db, "pa1", "u-a", "s1")
	seedKU(t, db, "u-b", "s1", "concept b topic", sql.NullString{String: conceptB, Valid: true})
	seedKP(t, db, "pb1", "u-b", "s1")
}

func TestScanMergeCandidates_BothConditionsMet_CreatesCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})

	for i := 0; i < 3; i++ {
		seedTrace(t, db, uuidTraceID(i), "h", []string{"pa1", "pb1"}, 0)
	}

	summary := svc.Scan()
	if summary.MergeCreated != 1 {
		t.Fatalf("expected 1 merge candidate created, got %d (summary=%+v)", summary.MergeCreated, summary)
	}

	candidates, err := store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("expected 1 pending merge candidate, got %d (err=%v)", len(candidates), err)
	}
	mf := candidateMergeFrom(t, &candidates[0])
	if len(mf) != 2 {
		t.Fatalf("expected merge_from with 2 entries, got %v", mf)
	}
	ev := candidateEvidence(t, &candidates[0])
	if int(ev["cooccur_count"].(float64)) != 3 {
		t.Errorf("evidence cooccur_count = %v, want 3", ev["cooccur_count"])
	}
	if ev["overlap_ratio"].(float64) != 1.0 {
		t.Errorf("evidence overlap_ratio = %v, want 1.0", ev["overlap_ratio"])
	}
}

func uuidTraceID(i int) string {
	return "trace-merge-" + string(rune('a'+i))
}

func TestScanMergeCandidates_CooccurNotMet_NoCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})

	// Only 2 co-occurring traces, MergeCooccurMin=3.
	seedTrace(t, db, "tr1", "h", []string{"pa1", "pb1"}, 0)
	seedTrace(t, db, "tr2", "h", []string{"pa1", "pb1"}, 0)

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no merge candidate (cooccur below threshold), got %d", len(candidates))
	}
}

func TestScanMergeCandidates_OverlapNotMet_NoCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})

	// 3 traces co-occur both entries (meets MergeCooccurMin=3), but each
	// concept also appears independently many more times, so the
	// trace-occurrence overlap coefficient (cooccur / |A ∪ B|) stays low:
	// cooccur=3, totalA=totalB=13, union=13+13-3=23, overlap=3/23≈0.13 < 0.5.
	for i := 0; i < 3; i++ {
		seedTrace(t, db, uuidTraceID(i), "h", []string{"pa1", "pb1"}, 0)
	}
	for i := 0; i < 10; i++ {
		seedTrace(t, db, fmt.Sprintf("tr-only-a-%d", i), "ha", []string{"pa1"}, 0)
		seedTrace(t, db, fmt.Sprintf("tr-only-b-%d", i), "hb", []string{"pb1"}, 0)
	}

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no merge candidate (overlap coefficient below threshold), got %d", len(candidates))
	}
}

func TestScanMergeCandidates_ExcludesMergedConcept(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, "cTarget", "d1", sql.NullString{})
	seedEntry(t, db, "cMerged", "d1", sql.NullString{String: "cTarget", Valid: true})
	seedEntry(t, db, "cB", "d1", sql.NullString{})
	seedKU(t, db, "u-m", "s1", "merged topic", sql.NullString{String: "cMerged", Valid: true})
	seedKP(t, db, "pm1", "u-m", "s1")
	seedKU(t, db, "u-b", "s1", "b topic", sql.NullString{String: "cB", Valid: true})
	seedKP(t, db, "pb1", "u-b", "s1")

	for i := 0; i < 3; i++ {
		seedTrace(t, db, uuidTraceID(i), "h", []string{"pm1", "pb1"}, 0)
	}

	svc.Scan()
	candidates, _ := store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if len(candidates) != 0 {
		t.Fatalf("expected no merge candidate (merged concept excluded from statistics), got %d", len(candidates))
	}
}

// --- Idle expiry ---

func TestScan_ExpiresIdlePendingCandidates(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed", sql.NullString{})
	if err != nil {
		t.Fatalf("insert add candidate: %v", err)
	}
	if _, err := db.Exec(`UPDATE entry_candidates SET last_signal_at = datetime('now', '-100 days') WHERE candidate_id = ?`, candidateID); err != nil {
		t.Fatal(err)
	}

	summary := svc.Scan()
	if summary.Expired != 1 {
		t.Fatalf("expected 1 expired candidate, got %d", summary.Expired)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	if c.Status != StatusExpired {
		t.Errorf("status = %q, want expired", c.Status)
	}

	var lrStatus string
	err = db.QueryRow(`SELECT status FROM learning_results WHERE object_type = 'entry_candidate' AND object_id = ?`, candidateID).Scan(&lrStatus)
	if err != nil {
		t.Fatalf("query learning_result: %v", err)
	}
	if lrStatus != "expired" {
		t.Errorf("learning_result status = %q, want expired", lrStatus)
	}
}

// --- Confirm / Reject ---

func TestConfirm_Add_CreatesConceptAndMigratesOnlyNullKUs(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{}) // NULL concept -> should migrate
	seedKP(t, db, "p1", "u1", "s1")
	seedEntry(t, db, "cOther", "d1", sql.NullString{})
	seedKU(t, db, "u2", "s1", "topic2", sql.NullString{String: "cOther", Valid: true}) // already anchored -> must not change
	seedKP(t, db, "p2", "u2", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "并发编程", EntryKindConcept, []string{"p1", "p2"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed", sql.NullString{})
	if err != nil {
		t.Fatalf("insert add candidate: %v", err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if result.EntryID == "" {
		t.Fatal("expected new entry_id")
	}
	if result.MigratedKUs != 1 {
		t.Errorf("expected 1 migrated KU (only the NULL one), got %d", result.MigratedKUs)
	}

	var origin string
	if err := db.QueryRow(`SELECT origin FROM entries WHERE entry_id = ?`, result.EntryID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "evolved" {
		t.Errorf("origin = %q, want evolved", origin)
	}

	var u1Entry, u2Entry sql.NullString
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&u1Entry)
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u2'`).Scan(&u2Entry)
	if !u1Entry.Valid || u1Entry.String != result.EntryID {
		t.Errorf("u1.entry_id = %+v, want %s", u1Entry, result.EntryID)
	}
	if !u2Entry.Valid || u2Entry.String != "cOther" {
		t.Errorf("u2.entry_id changed unexpectedly: %+v, want cOther", u2Entry)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusApplied {
		t.Errorf("candidate status = %q, want applied", c.Status)
	}
	var lrStatus, confirmedBy string
	db.QueryRow(`SELECT status, confirmed_by FROM learning_results WHERE object_type = 'entry_candidate' AND object_id = ?`, candidateID).
		Scan(&lrStatus, &confirmedBy)
	if lrStatus != "applied" || confirmedBy != "manual" {
		t.Errorf("learning_result status/confirmed_by = %q/%q, want applied/manual", lrStatus, confirmedBy)
	}

	// Second confirm must fail: no longer pending_confirm.
	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil); err == nil {
		t.Error("expected error confirming an already-applied candidate")
	}
}

// TestConfirmAdd_FactCandidate_PersistsParentEntryID covers
// docs/impl/v1/fact-entry-parent-concept-task-brief.md: a fact candidate's
// parent_entry_id (the concept it was classified under at generation time)
// must survive confirm onto the new entries row, and be visible via
// ListActiveEntries; a concept candidate (no parent) must confirm with
// parent_entry_id left empty.
func TestConfirmAdd_FactCandidate_PersistsParentEntryID(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, "c-backup", "d1", sql.NullString{})
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	factCandidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "MySQL备份", EntryKindFact,
		[]string{"p1"}, nil, AddEvidence{}, "seed", sql.NullString{String: "c-backup", Valid: true})
	if err != nil {
		t.Fatalf("insert fact candidate: %v", err)
	}
	result, err := svc.Confirm(factCandidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatalf("confirm fact candidate: %v", err)
	}

	var parentEntryID sql.NullString
	if err := db.QueryRow(`SELECT parent_entry_id FROM entries WHERE entry_id = ?`, result.EntryID).Scan(&parentEntryID); err != nil {
		t.Fatal(err)
	}
	if !parentEntryID.Valid || parentEntryID.String != "c-backup" {
		t.Errorf("entries.parent_entry_id = %+v, want c-backup", parentEntryID)
	}

	infos, err := svc.ListActiveEntries("d1")
	if err != nil {
		t.Fatalf("list active entries: %v", err)
	}
	found := false
	for _, info := range infos {
		if info.EntryID == result.EntryID {
			found = true
			if info.ParentEntryID != "c-backup" {
				t.Errorf("EntryInfo.ParentEntryID = %q, want c-backup", info.ParentEntryID)
			}
		}
	}
	if !found {
		t.Fatalf("new fact entry %s not found in ListActiveEntries", result.EntryID)
	}

	// A concept candidate has no parent — must confirm with parent_entry_id
	// left NULL, not e.g. defaulted to the fact case's value.
	seedKU(t, db, "u2", "s1", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s1")
	conceptCandidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "并发编程", EntryKindConcept,
		[]string{"p2"}, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatalf("insert concept candidate: %v", err)
	}
	conceptResult, err := svc.Confirm(conceptCandidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatalf("confirm concept candidate: %v", err)
	}
	var conceptParent sql.NullString
	if err := db.QueryRow(`SELECT parent_entry_id FROM entries WHERE entry_id = ?`, conceptResult.EntryID).Scan(&conceptParent); err != nil {
		t.Fatal(err)
	}
	if conceptParent.Valid {
		t.Errorf("concept entries.parent_entry_id = %+v, want NULL", conceptParent)
	}
}

func TestConfirm_Add_NoDomain_RequiresOverride(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil); err == nil {
		t.Fatal("expected error: no domain_id available")
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{DomainID: "d1"}, nil)
	if err != nil {
		t.Fatalf("confirm with override domain: %v", err)
	}
	if result.EntryID == "" {
		t.Error("expected concept created with overridden domain")
	}
}

func TestConfirm_Merge_MigratesAndFlagsWikiPages(t *testing.T) {
	svc, store, db, wikiSvc := setupServiceWithWiki(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})

	insertWikiPage(t, db, "page-a", "cA")
	insertWikiPage(t, db, "page-b", "cB")

	candidateID, err := store.InsertMergeCandidate([]string{"cA", "cB"}, []string{"pa1", "pb1"}, MergeEvidence{CooccurCount: 5, OverlapRatio: 0.8}, "seed")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.Confirm(candidateID, nil, &ConfirmMergeRequest{Target: "cA"})
	if err != nil {
		t.Fatalf("confirm merge: %v", err)
	}
	if result.MigratedKUs != 1 {
		t.Errorf("expected 1 migrated KU (u-b), got %d", result.MigratedKUs)
	}
	if result.FlaggedPages != 2 {
		t.Errorf("expected 2 flagged wiki pages (both entries), got %d", result.FlaggedPages)
	}

	var uBEntry string
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u-b'`).Scan(&uBEntry)
	if uBEntry != "cA" {
		t.Errorf("u-b.entry_id = %q, want cA", uBEntry)
	}

	var mergedInto sql.NullString
	db.QueryRow(`SELECT merged_into FROM entries WHERE entry_id = 'cB'`).Scan(&mergedInto)
	if !mergedInto.Valid || mergedInto.String != "cA" {
		t.Errorf("cB.merged_into = %+v, want cA", mergedInto)
	}

	pageA, err := wikiSvc.GetActivePageByEntryID("cA")
	if err != nil || pageA == nil || pageA.Status != "needs_recompile" {
		t.Errorf("page-a status not needs_recompile: %+v (err=%v)", pageA, err)
	}
}

func TestConfirm_Merge_InvalidTarget_Errors(t *testing.T) {
	svc, store, db := setupService(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})

	candidateID, err := store.InsertMergeCandidate([]string{"cA", "cB"}, []string{"pa1", "pb1"}, MergeEvidence{}, "seed")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, nil, &ConfirmMergeRequest{Target: "cNotInPair"}); err == nil {
		t.Fatal("expected error for target not in merge_from")
	}
}

// --- KPN content_driven candidates (docs/impl/v1/kpn.md 步骤 3/6) ---

func TestCreateManualCandidate_BlankThenConfirmable(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	// Blank args model the never-touched "新增" draft form — nothing to
	// persist yet until the user actually fills something in.
	candidateID, err := svc.CreateManualCandidate("", "", "", "", nil)
	if err != nil {
		t.Fatalf("create manual candidate: %v", err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	if c.Kind != KindAdd || c.Status != StatusPendingConfirm {
		t.Errorf("kind/status = %s/%s, want add/pending_confirm", c.Kind, c.Status)
	}
	if c.DomainID.Valid || c.SuggestedName.String != "" || c.PointIDs != "[]" {
		t.Errorf("expected blank domain/name/point_ids, got domain=%+v name=%q point_ids=%q", c.DomainID, c.SuggestedName.String, c.PointIDs)
	}
	ev := candidateEvidence(t, c)
	if ev["origin"] != "manual" {
		t.Errorf("evidence.origin = %v, want manual", ev["origin"])
	}

	// The same dialog/API used for every other add candidate must be able
	// to fill in the blanks and confirm it — no separate creation path.
	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{DomainID: "d1", SuggestedName: "新概念", PointIDs: []string{"p1"}}, nil)
	if err != nil {
		t.Fatalf("confirm manual candidate: %v", err)
	}
	if result.EntryID == "" || result.MigratedKUs != 1 {
		t.Errorf("unexpected confirm result: %+v", result)
	}
}

func TestCreateManualCandidate_FilledFromDraft(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	// The draft dialog's own "确认" (保存到待确认) sends whatever the user
	// filled in — this is what CreateManualCandidate now actually receives.
	candidateID, err := svc.CreateManualCandidate("d1", "并发编程", "", EntryKindConcept, []string{"p1"})
	if err != nil {
		t.Fatalf("create manual candidate: %v", err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	if !c.DomainID.Valid || c.DomainID.String != "d1" {
		t.Errorf("domain_id = %+v, want d1", c.DomainID)
	}
	if c.SuggestedName.String != "并发编程" {
		t.Errorf("suggested_name = %q, want 并发编程", c.SuggestedName.String)
	}
	if len(candidatePointIDs(t, c)) != 1 {
		t.Errorf("expected 1 point_id, got %v", candidatePointIDs(t, c))
	}
}

func TestProposeAddCandidate_FirstCall_CreatesContentDrivenCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "差旅报销", "关注差旅费用报销标准", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("propose add candidate: %v", err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	if c.Kind != KindAdd || c.Status != StatusPendingConfirm {
		t.Errorf("kind/status = %s/%s, want add/pending_confirm", c.Kind, c.Status)
	}
	if c.SuggestedName.String != "差旅报销" {
		t.Errorf("suggested_name = %q, want 差旅报销", c.SuggestedName.String)
	}
	ev := candidateEvidence(t, c)
	if ev["origin"] != "content_driven" {
		t.Errorf("evidence.origin = %v, want content_driven", ev["origin"])
	}
	if len(candidatePointIDs(t, c)) != 1 {
		t.Errorf("expected 1 point_id, got %v", candidatePointIDs(t, c))
	}
}

func TestListPendingAddPointIDs_ReturnsDomainPendingPoints(t *testing.T) {
	svc, _, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedKP(t, db, "p2", "u1", "s1")

	if _, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc", "", EntryKindConcept, "", []string{"p1", "p2"}, "s1", ""); err != nil {
		t.Fatalf("propose: %v", err)
	}

	ids, err := svc.ListPendingAddPointIDs("d1")
	if err != nil {
		t.Fatalf("ListPendingAddPointIDs: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["p1"] || !got["p2"] {
		t.Errorf("ids = %v, want p1 and p2", ids)
	}
	other, err := svc.ListPendingAddPointIDs("d-other")
	if err != nil {
		t.Fatalf("ListPendingAddPointIDs other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("other domain ids = %v, want empty", other)
	}
}

func TestProposeAddCandidate_SecondCallSameDomainSameName_MergesIntoExisting(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic1", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedSource(t, db, "s2", "d1")
	seedKU(t, db, "u2", "s2", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s2")

	id1, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc1", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	id2, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc2", "", EntryKindConcept, "", []string{"p2"}, "s2", "")
	if err != nil {
		t.Fatalf("second propose: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected second call to merge into the first candidate, got distinct ids %s / %s", id1, id2)
	}

	c, _ := store.GetCandidate(id1)
	if c.SuggestedName.String != "差旅报销" {
		t.Errorf("suggested_name drifted to %q, want it to keep the original 差旅报销", c.SuggestedName.String)
	}
	points := candidatePointIDs(t, c)
	if len(points) != 2 {
		t.Fatalf("expected merged point_ids from both calls, got %v", points)
	}

	rows, err := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 pending add candidate (no duplicate row), got %d", len(rows))
	}
}

// TestProposeAddCandidate_MergeWithDifferentEntity_RecordsAlias covers the
// 2026-08-05 alias-accumulation addition: a second batch merging into the
// same pending fact candidate under a different entity string (e.g. a typo
// variant, or a different name for the same real-world product) should be
// recorded as an alias rather than silently dropped, while the
// first-seen entity stays the canonical one.
func TestProposeAddCandidate_MergeWithDifferentEntity_RecordsAlias(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic1", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedSource(t, db, "s2", "d1")
	seedKU(t, db, "u2", "s2", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s2")

	id1, err := svc.ProposeAddCandidate("d1", "MySQL备份", "desc1", "", EntryKindFact, "MySQL", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	id2, err := svc.ProposeAddCandidate("d1", "MySQL备份", "desc2", "", EntryKindFact, "mysql数据库", []string{"p2"}, "s2", "")
	if err != nil {
		t.Fatalf("second propose: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected merge into the same candidate, got distinct ids %s / %s", id1, id2)
	}

	c, _ := store.GetCandidate(id1)
	var ev ContentDrivenEvidence
	if err := json.Unmarshal([]byte(c.Evidence), &ev); err != nil {
		t.Fatalf("unmarshal evidence: %v", err)
	}
	if ev.Entity != "MySQL" {
		t.Errorf("entity = %q, want first-seen entity MySQL to stay canonical", ev.Entity)
	}
	if len(ev.Aliases) != 1 || ev.Aliases[0] != "mysql数据库" {
		t.Errorf("aliases = %v, want [mysql数据库]", ev.Aliases)
	}
}

// TestConfirmAdd_WritesBoundaryAndAliases covers the 2026-08-05 additions:
// confirming a kind=add candidate must carry its evidence.boundary and
// evidence.aliases through to the new entries row, not just name/
// description/kind.
func TestConfirmAdd_WritesBoundaryAndAliases(t *testing.T) {
	svc, _, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic1", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedSource(t, db, "s2", "d1")
	seedKU(t, db, "u2", "s2", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s2")

	candidateID, err := svc.ProposeAddCandidate("d1", "达梦数据库备份", "desc", "关注达梦数据库的备份操作", EntryKindFact, "达梦数据库", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := svc.ProposeAddCandidate("d1", "达梦数据库备份", "desc", "关注达梦数据库的备份操作", EntryKindFact, "DM数据库", []string{"p2"}, "s2", ""); err != nil {
		t.Fatalf("second propose: %v", err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{Description: "desc"}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	var boundary, aliasesJSON string
	if err := db.QueryRow(`SELECT boundary, aliases FROM entries WHERE entry_id = ?`, result.EntryID).Scan(&boundary, &aliasesJSON); err != nil {
		t.Fatal(err)
	}
	if boundary != "关注达梦数据库的备份操作" {
		t.Errorf("boundary = %q, want it carried from evidence.boundary", boundary)
	}
	var aliases []string
	if err := json.Unmarshal([]byte(aliasesJSON), &aliases); err != nil {
		t.Fatalf("unmarshal aliases: %v", err)
	}
	if len(aliases) != 1 || aliases[0] != "DM数据库" {
		t.Errorf("aliases = %v, want [DM数据库]", aliases)
	}
}

// TestProposeAddCandidate_SecondCallSameDomainDifferentName_DoesNotMerge
// guards against the bug where kpn_entry_propose.md's multi-cluster
// output (docs/impl/v1/kpn.md 步骤 3) all collapsed into one candidate row
// because the old merge key was domain-only: a single "+ 新增概念" click
// that named several distinct clusters in the same domain must produce
// several distinct pending_confirm candidates, not one candidate holding
// every orphan KP under whichever name happened to be proposed first.
func TestProposeAddCandidate_SecondCallSameDomainDifferentName_DoesNotMerge(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic1", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedSource(t, db, "s2", "d1")
	seedKU(t, db, "u2", "s2", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s2")

	id1, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc1", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	id2, err := svc.ProposeAddCandidate("d1", "住宿标准", "desc2", "", EntryKindConcept, "", []string{"p2"}, "s2", "")
	if err != nil {
		t.Fatalf("second propose: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct candidates for distinct suggested_name in the same domain, got same id %s", id1)
	}

	rows, err := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct pending add candidates, got %d", len(rows))
	}
}

func TestProposeAddCandidate_DoesNotMergeIntoUsageDrivenCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	// A usage_driven candidate already pending in the same domain.
	if _, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "既有候选", EntryKindConcept, []string{"pX"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed", sql.NullString{}); err != nil {
		t.Fatal(err)
	}

	candidateID, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("propose add candidate: %v", err)
	}

	rows, err := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct pending candidates (usage_driven untouched), got %d", len(rows))
	}
	c, _ := store.GetCandidate(candidateID)
	if c.SuggestedName.String != "差旅报销" {
		t.Errorf("expected a new content_driven candidate, got merged into 既有候选")
	}
}

// fakeKPNRematchNotifier stands in for internal/unit.Service in concept
// package tests (docs/impl/v1/kpn.md 步骤 6).
type fakeKPNRematchNotifier struct {
	calls               []fakeRematchCall
	returnRelationIDs   []string
	orphanDomainIDs     []string
	orphanReturnTouched int
	orphanErr           error
}

type fakeRematchCall struct {
	conceptID string
	pointIDs  []string
}

func (f *fakeKPNRematchNotifier) RematchPoints(conceptID string, pointIDs []string) []string {
	f.calls = append(f.calls, fakeRematchCall{conceptID, pointIDs})
	return f.returnRelationIDs
}

func (f *fakeKPNRematchNotifier) ProposeEntriesForDomainOrphans(ctx context.Context, domainID string) (int, error) {
	f.orphanDomainIDs = append(f.orphanDomainIDs, domainID)
	return f.orphanReturnTouched, f.orphanErr
}

func TestProposeEntriesFromDomainOrphans_DelegatesToNotifier(t *testing.T) {
	svc, _, _ := setupService(t)
	notifier := &fakeKPNRematchNotifier{orphanReturnTouched: 3}
	svc.SetKPNRematchNotifier(notifier)

	touched, err := svc.ProposeEntriesFromDomainOrphans(context.Background(), "d1")
	if err != nil {
		t.Fatalf("ProposeEntriesFromDomainOrphans: %v", err)
	}
	if touched != 3 {
		t.Errorf("touched = %d, want 3", touched)
	}
	if len(notifier.orphanDomainIDs) != 1 || notifier.orphanDomainIDs[0] != "d1" {
		t.Errorf("expected delegation with domain_id d1, got %v", notifier.orphanDomainIDs)
	}
}

func TestProposeEntriesFromDomainOrphans_ErrorsWithoutNotifier(t *testing.T) {
	svc, _, _ := setupService(t)

	if _, err := svc.ProposeEntriesFromDomainOrphans(context.Background(), "d1"); err != ErrNoOrphanClusterer {
		t.Errorf("expected ErrNoOrphanClusterer, got %v", err)
	}
}

func TestConfirmAdd_Assign_MigratesToExistingConceptWithoutCreatingNew(t *testing.T) {
	svc, store, db := setupService(t)
	notifier := &fakeKPNRematchNotifier{}
	svc.SetKPNRematchNotifier(notifier)

	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, "cExisting", "d1", sql.NullString{})
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}

	var conceptCountBefore int
	db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&conceptCountBefore)

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "cExisting"}, nil)
	if err != nil {
		t.Fatalf("confirm assign: %v", err)
	}
	if result.EntryID != "cExisting" {
		t.Errorf("entry_id = %q, want cExisting", result.EntryID)
	}
	if result.MigratedKUs != 1 {
		t.Errorf("migrated KUs = %d, want 1", result.MigratedKUs)
	}

	var conceptCountAfter int
	db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&conceptCountAfter)
	if conceptCountAfter != conceptCountBefore {
		t.Errorf("expected no new concept row, before=%d after=%d", conceptCountBefore, conceptCountAfter)
	}

	var u1Entry sql.NullString
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&u1Entry)
	if !u1Entry.Valid || u1Entry.String != "cExisting" {
		t.Errorf("u1.entry_id = %+v, want cExisting", u1Entry)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusApplied {
		t.Errorf("candidate status = %q, want applied", c.Status)
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 RematchPoints call, got %d", len(notifier.calls))
	}
	if notifier.calls[0].conceptID != "cExisting" || len(notifier.calls[0].pointIDs) != 1 || notifier.calls[0].pointIDs[0] != "p1" {
		t.Errorf("unexpected rematch call: %+v", notifier.calls[0])
	}
}

func TestConfirmAdd_Assign_RejectsMergedAwayConcept(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, "cTarget", "d1", sql.NullString{})
	seedEntry(t, db, "cMergedAway", "d1", sql.NullString{String: "cTarget", Valid: true})
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, nil, ContentDrivenEvidence{Origin: "content_driven"}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "cMergedAway"}, nil); err == nil {
		t.Fatal("expected error assigning to a merged-away concept")
	}
	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "does-not-exist"}, nil); err == nil {
		t.Fatal("expected error assigning to a nonexistent concept")
	}
}

func TestConfirmAdd_New_NotifiesKPNRematch(t *testing.T) {
	svc, _, db := setupService(t)
	notifier := &fakeKPNRematchNotifier{}
	svc.SetKPNRematchNotifier(notifier)

	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].conceptID != result.EntryID {
		t.Fatalf("expected 1 RematchPoints call for the new entry_id, got %+v", notifier.calls)
	}
}

func TestReject_MarksRejectedNoStructuralChange(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Reject(candidateID); err != nil {
		t.Fatalf("reject: %v", err)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusRejected {
		t.Errorf("status = %q, want rejected", c.Status)
	}

	var conceptID sql.NullString
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&conceptID)
	if conceptID.Valid {
		t.Errorf("expected u1.entry_id to remain NULL after reject, got %+v", conceptID)
	}

	if err := svc.Reject(candidateID); err == nil {
		t.Error("expected error rejecting an already-rejected candidate")
	}
}

func TestDelete_RemovesPendingCandidateAndLearningResult(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(candidateID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	c, _ := store.GetCandidate(candidateID)
	if c != nil {
		t.Errorf("expected candidate row gone, got %+v", c)
	}

	var lrCount int
	db.QueryRow(`SELECT COUNT(*) FROM learning_results WHERE object_type = 'entry_candidate' AND object_id = ?`, candidateID).Scan(&lrCount)
	if lrCount != 0 {
		t.Errorf("expected learning_result row gone, got %d", lrCount)
	}

	if err := svc.Delete(candidateID); err == nil {
		t.Error("expected error deleting an already-deleted candidate")
	}
}

func TestDelete_RejectsNonPendingCandidate(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Reject(candidateID); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(candidateID); err == nil {
		t.Error("expected error deleting a rejected candidate")
	}
}

func TestRestore_Rejected_ReturnsToPendingConfirm(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept, []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Reject(candidateID); err != nil {
		t.Fatal(err)
	}

	if err := svc.Restore(candidateID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusPendingConfirm {
		t.Errorf("status = %q, want pending_confirm", c.Status)
	}

	var lrStatus string
	db.QueryRow(`SELECT status FROM learning_results WHERE object_type = 'entry_candidate' AND object_id = ?`, candidateID).Scan(&lrStatus)
	if lrStatus != "pending_confirm" {
		t.Errorf("learning_result status = %q, want pending_confirm", lrStatus)
	}

	if err := svc.Restore(candidateID); err == nil {
		t.Error("expected error restoring an already-pending candidate")
	}
}

func TestRestore_AppliedNewConcept_DeletesConceptAndRevertsKUs(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "并发编程", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conceptID := result.EntryID

	if err := svc.Restore(candidateID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusPendingConfirm {
		t.Errorf("status = %q, want pending_confirm", c.Status)
	}
	if c.ResolvedEntryID.Valid || c.CreatedNewEntry {
		t.Errorf("expected resolved_entry_id cleared and created_new_entry=false, got %+v/%v", c.ResolvedEntryID, c.CreatedNewEntry)
	}

	var conceptCount int
	db.QueryRow(`SELECT COUNT(*) FROM entries WHERE entry_id = ?`, conceptID).Scan(&conceptCount)
	if conceptCount != 0 {
		t.Errorf("expected concept row deleted, still found %d", conceptCount)
	}

	var u1Entry sql.NullString
	db.QueryRow(`SELECT entry_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&u1Entry)
	if u1Entry.Valid {
		t.Errorf("expected u1.entry_id reverted to NULL, got %+v", u1Entry)
	}

	var lrStatus string
	db.QueryRow(`SELECT status FROM learning_results WHERE object_type = 'entry_candidate' AND object_id = ?`, candidateID).Scan(&lrStatus)
	if lrStatus != "pending_confirm" {
		t.Errorf("learning_result status = %q, want pending_confirm", lrStatus)
	}
}

func TestRestore_AppliedNewConcept_DeletesOnlyThisConfirmsKPNRelations(t *testing.T) {
	svc, store, db := setupService(t)
	notifier := &fakeKPNRematchNotifier{returnRelationIDs: []string{"rel-1"}}
	svc.SetKPNRematchNotifier(notifier)

	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedSource(t, db, "s2", "d1")
	seedKU(t, db, "u2", "s2", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s2")

	// rel-1 stands in for what RematchPoints would have actually inserted
	// (the fake just returns the id, doesn't insert it, so the row is seeded
	// directly). rel-unrelated has a different relation_type on the same
	// pair so it doesn't collide with idx_kp_relations_uniq, and represents
	// a relation this candidate's confirm did NOT create (e.g. a later,
	// unrelated Source import) — restore must leave it alone.
	if _, err := db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope) VALUES ('rel-1', 'p1', 'p2', 'related', 'bidirectional', 'v1', 'cross')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope) VALUES ('rel-unrelated', 'p1', 'p2', 'contradicts', 'bidirectional', 'v1', 'cross')`); err != nil {
		t.Fatal(err)
	}

	candidateID, err := svc.ProposeAddCandidate("d1", "并发编程", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil); err != nil {
		t.Fatal(err)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.KPNRelationIDs != `["rel-1"]` {
		t.Errorf("kpn_relation_ids = %q, want [\"rel-1\"]", c.KPNRelationIDs)
	}

	if err := svc.Restore(candidateID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM knowledge_point_relations WHERE relation_id = 'rel-1'`).Scan(&count)
	if count != 0 {
		t.Error("expected rel-1 (recorded on this candidate) deleted by restore")
	}
	db.QueryRow(`SELECT COUNT(*) FROM knowledge_point_relations WHERE relation_id = 'rel-unrelated'`).Scan(&count)
	if count != 1 {
		t.Error("expected rel-unrelated (not recorded on this candidate) to survive restore")
	}

	c, _ = store.GetCandidate(candidateID)
	if c.KPNRelationIDs != "[]" {
		t.Errorf("kpn_relation_ids after restore = %q, want []", c.KPNRelationIDs)
	}
}

func TestRestore_AppliedAssignToExisting_NotRestorable(t *testing.T) {
	svc, _, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedEntry(t, db, "cExisting", "d1", sql.NullString{})
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "差旅报销", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "cExisting"}, nil); err != nil {
		t.Fatal(err)
	}

	if err := svc.Restore(candidateID); err == nil {
		t.Error("expected error restoring an assign-to-existing-concept candidate")
	}
}

func TestRestore_AppliedNewConcept_RefusesWhenLaterConfirmAssignedMoreKUs(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	seedKU(t, db, "u2", "s1", "topic2", sql.NullString{})
	seedKP(t, db, "p2", "u2", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "并发编程", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	otherCandidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "并发编程2", EntryKindConcept, []string{"p2"}, nil, ContentDrivenEvidence{Origin: "content_driven"}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(otherCandidateID, &ConfirmAddRequest{EntryID: result.EntryID}, nil); err != nil {
		t.Fatal(err)
	}

	if err := svc.Restore(candidateID); err == nil {
		t.Error("expected error restoring while another KU still references the concept")
	}
}

func TestRestore_AppliedNewConcept_RefusesWhenActiveWikiPageExists(t *testing.T) {
	svc, store, db, _ := setupServiceWithWiki(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "并发编程", "desc", "", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	insertWikiPage(t, db, "page-1", result.EntryID)

	if err := svc.Restore(candidateID); err == nil {
		t.Error("expected error restoring while an active wiki page references the concept")
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusApplied {
		t.Errorf("status should remain applied when restore is refused, got %q", c.Status)
	}
}
