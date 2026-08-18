package entry

import (
	"database/sql"
	"testing"
)

func testConfigAutoConfirm() Config {
	cfg := testConfig()
	cfg.AutoConfirmAdd = true
	return cfg
}

func setupServiceAutoConfirm(t *testing.T) (*Service, *Store, *sql.DB) {
	t.Helper()
	svc, store, db := setupService(t)
	svc.cfg = testConfigAutoConfirm()
	return svc, store, db
}

// TestScanAddClusters_AutoConfirm_AppliesImmediately verifies the
// usage-driven (Study 扫描) creation path: once cfg.AutoConfirmAdd is set,
// a newly created kind=add candidate should be applied (a live entry
// created, candidate status=applied) without any separate Confirm call —
// 2026-08-14 改判, docs/design/concept-evolution.md "2026-08-14 改判".
func TestScanAddClusters_AutoConfirm_AppliesImmediately(t *testing.T) {
	svc, store, db := setupServiceAutoConfirm(t)

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

	pending, err := store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		t.Fatalf("ListCandidatesByKindStatus: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 pending candidates after auto-confirm, got %d", len(pending))
	}

	applied, err := store.ListCandidatesByKindStatus(KindAdd, StatusApplied)
	if err != nil {
		t.Fatalf("ListCandidatesByKindStatus(applied): %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected 1 applied candidate, got %d", len(applied))
	}
	if !applied[0].ResolvedEntryID.Valid || applied[0].ResolvedEntryID.String == "" {
		t.Error("expected applied candidate to have a resolved entry_id")
	}
}

// TestProposeAddCandidate_AutoConfirm_AppliesImmediately verifies the
// content-driven (KPN 匹配) creation path behaves the same way.
func TestProposeAddCandidate_AutoConfirm_AppliesImmediately(t *testing.T) {
	svc, store, db := setupServiceAutoConfirm(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "并发编程", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "并发编程", "描述", "边界", EntryKindConcept, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatalf("ProposeAddCandidate: %v", err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil {
		t.Fatalf("GetCandidate: %v", err)
	}
	if c.Status != StatusApplied {
		t.Errorf("expected status=applied, got %s", c.Status)
	}
}

// TestCreateManualCandidate_AutoConfirm_AppliesImmediately verifies the
// manual "新增" draft path behaves the same way.
func TestCreateManualCandidate_AutoConfirm_AppliesImmediately(t *testing.T) {
	svc, store, db := setupServiceAutoConfirm(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "手动新增", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.CreateManualCandidate("d1", "手动概念", "手动描述", EntryKindFact, []string{"p1"})
	if err != nil {
		t.Fatalf("CreateManualCandidate: %v", err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil {
		t.Fatalf("GetCandidate: %v", err)
	}
	if c.Status != StatusApplied {
		t.Errorf("expected status=applied, got %s", c.Status)
	}
}

// TestScanMergeCandidates_AutoConfirm_StaysPending verifies kind=merge
// candidates are NOT auto-confirmed even when AutoConfirmAdd is set —
// merges restructure existing concepts rather than creating new ones, and
// remain human-gated regardless of this flag.
func TestScanMergeCandidates_AutoConfirm_StaysPending(t *testing.T) {
	svc, store, db := setupServiceAutoConfirm(t)
	seedMergeScenario(t, db, "cA", "cB", sql.NullString{})
	for i := 0; i < 3; i++ {
		seedTrace(t, db, uuidTraceID(i), "h", []string{"pa1", "pb1"}, 0)
	}

	summary := svc.Scan()
	if summary.MergeCreated != 1 {
		t.Fatalf("expected 1 merge candidate created, got %d (summary=%+v)", summary.MergeCreated, summary)
	}

	pending, err := store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if err != nil {
		t.Fatalf("ListCandidatesByKindStatus: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected merge candidate to stay pending_confirm even with AutoConfirmAdd set, got %d pending", len(pending))
	}
}
