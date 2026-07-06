package concept

import (
	"database/sql"
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
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h2", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h3", pointIDs, "concept_gap")

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
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h2", pointIDs, "concept_gap")
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
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")

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
	seedConceptGapEvent(t, db, newEventID(), "h1", []string{"p1"}, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h2", []string{"p2"}, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h3", []string{"p3"}, "concept_gap")

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
	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "link_gap")
	seedConceptGapEvent(t, db, newEventID(), "h2", pointIDs, "link_gap")
	seedConceptGapEvent(t, db, newEventID(), "h3", pointIDs, "link_gap")

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

	seedConceptGapEvent(t, db, newEventID(), "h1", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h2", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h3", pointIDs, "concept_gap")
	summary1 := svc.Scan()
	if summary1.AddCreated != 1 {
		t.Fatalf("expected 1 candidate created in cycle 1, got %d", summary1.AddCreated)
	}

	before, _ := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	beforeEventCount := len(candidateEventIDs(t, &before[0]))

	// Cycle 2: two more overlapping events arrive.
	seedConceptGapEvent(t, db, newEventID(), "h4", pointIDs, "concept_gap")
	seedConceptGapEvent(t, db, newEventID(), "h5", pointIDs, "concept_gap")
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
	seedConcept(t, db, conceptA, "d1", sql.NullString{})
	seedConcept(t, db, conceptB, "d1", mergedInto)
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
		t.Fatalf("expected merge_from with 2 concepts, got %v", mf)
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

	// 3 traces co-occur both concepts (meets MergeCooccurMin=3), but each
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
	seedConcept(t, db, "cTarget", "d1", sql.NullString{})
	seedConcept(t, db, "cMerged", "d1", sql.NullString{String: "cTarget", Valid: true})
	seedConcept(t, db, "cB", "d1", sql.NullString{})
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

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed")
	if err != nil {
		t.Fatalf("insert add candidate: %v", err)
	}
	if _, err := db.Exec(`UPDATE concept_candidates SET last_signal_at = datetime('now', '-100 days') WHERE candidate_id = ?`, candidateID); err != nil {
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
	err = db.QueryRow(`SELECT status FROM learning_results WHERE object_type = 'concept_candidate' AND object_id = ?`, candidateID).Scan(&lrStatus)
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
	seedConcept(t, db, "cOther", "d1", sql.NullString{})
	seedKU(t, db, "u2", "s1", "topic2", sql.NullString{String: "cOther", Valid: true}) // already anchored -> must not change
	seedKP(t, db, "p2", "u2", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "并发编程", []string{"p1", "p2"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed")
	if err != nil {
		t.Fatalf("insert add candidate: %v", err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if result.ConceptID == "" {
		t.Fatal("expected new concept_id")
	}
	if result.MigratedKUs != 1 {
		t.Errorf("expected 1 migrated KU (only the NULL one), got %d", result.MigratedKUs)
	}

	var origin string
	if err := db.QueryRow(`SELECT origin FROM concepts WHERE concept_id = ?`, result.ConceptID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "evolved" {
		t.Errorf("origin = %q, want evolved", origin)
	}

	var u1Concept, u2Concept sql.NullString
	db.QueryRow(`SELECT concept_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&u1Concept)
	db.QueryRow(`SELECT concept_id FROM knowledge_units WHERE unit_id = 'u2'`).Scan(&u2Concept)
	if !u1Concept.Valid || u1Concept.String != result.ConceptID {
		t.Errorf("u1.concept_id = %+v, want %s", u1Concept, result.ConceptID)
	}
	if !u2Concept.Valid || u2Concept.String != "cOther" {
		t.Errorf("u2.concept_id changed unexpectedly: %+v, want cOther", u2Concept)
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusApplied {
		t.Errorf("candidate status = %q, want applied", c.Status)
	}
	var lrStatus, confirmedBy string
	db.QueryRow(`SELECT status, confirmed_by FROM learning_results WHERE object_type = 'concept_candidate' AND object_id = ?`, candidateID).
		Scan(&lrStatus, &confirmedBy)
	if lrStatus != "applied" || confirmedBy != "manual" {
		t.Errorf("learning_result status/confirmed_by = %q/%q, want applied/manual", lrStatus, confirmedBy)
	}

	// Second confirm must fail: no longer pending_confirm.
	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{}, nil); err == nil {
		t.Error("expected error confirming an already-applied candidate")
	}
}

func TestConfirm_Add_NoDomain_RequiresOverride(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed")
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
	if result.ConceptID == "" {
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
		t.Errorf("expected 2 flagged wiki pages (both concepts), got %d", result.FlaggedPages)
	}

	var uBConcept string
	db.QueryRow(`SELECT concept_id FROM knowledge_units WHERE unit_id = 'u-b'`).Scan(&uBConcept)
	if uBConcept != "cA" {
		t.Errorf("u-b.concept_id = %q, want cA", uBConcept)
	}

	var mergedInto sql.NullString
	db.QueryRow(`SELECT merged_into FROM concepts WHERE concept_id = 'cB'`).Scan(&mergedInto)
	if !mergedInto.Valid || mergedInto.String != "cA" {
		t.Errorf("cB.merged_into = %+v, want cA", mergedInto)
	}

	pageA, err := wikiSvc.GetActivePageByConceptID("cA")
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

func TestReject_MarksRejectedNoStructuralChange(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed")
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
	db.QueryRow(`SELECT concept_id FROM knowledge_units WHERE unit_id = 'u1'`).Scan(&conceptID)
	if conceptID.Valid {
		t.Errorf("expected u1.concept_id to remain NULL after reject, got %+v", conceptID)
	}

	if err := svc.Reject(candidateID); err == nil {
		t.Error("expected error rejecting an already-rejected candidate")
	}
}
