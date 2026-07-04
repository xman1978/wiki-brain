package activation

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return foundation.NewTestDB(t)
}

func seedSource(t *testing.T, db *sql.DB, sourceID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES (?, 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'done')`, sourceID)
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func seedKU(t *testing.T, db *sql.DB, unitID, sourceID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, 'test topic', 1, 10, 'done', 'v1')`, unitID, sourceID)
	if err != nil {
		t.Fatalf("seed KU: %v", err)
	}
}

func seedKP(t *testing.T, db *sql.DB, pointID, unitID, sourceID, content string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES (?, ?, ?, ?, 'fact')`, pointID, unitID, sourceID, content)
	if err != nil {
		t.Fatalf("seed KP: %v", err)
	}
}

func setLifecycle(t *testing.T, db *sql.DB, pointID, lifecycle string) {
	t.Helper()
	_, err := db.Exec(`UPDATE knowledge_points SET lifecycle = ? WHERE point_id = ?`, lifecycle, pointID)
	if err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}
}

// seedKPFull seeds source + KU + KP in one call for tests that don't care
// about the intermediate ids.
func seedKPFull(t *testing.T, db *sql.DB, pointID string) {
	t.Helper()
	seedSource(t, db, pointID+"-src")
	seedKU(t, db, pointID+"-ku", pointID+"-src")
	seedKP(t, db, pointID, pointID+"-ku", pointID+"-src", "content of "+pointID)
}

func TestStore_InsertAndGetByID(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedKPFull(t, db, "kp1")

	link := &ActivationLink{
		QuestionTerms: "t1",
		SubjectTerms:  "s1",
		PointID:       "kp1",
	}
	if err := store.InsertLink(link); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	if link.LinkID == "" {
		t.Fatal("expected generated link id")
	}

	got, err := store.GetByID(link.LinkID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got == nil {
		t.Fatal("expected link, got nil")
	}
	if got.Status != StatusCandidate {
		t.Errorf("status = %q, want candidate", got.Status)
	}
	if got.QuestionTerms != "t1" || got.SubjectTerms != "s1" {
		t.Errorf("unexpected fields: %+v", got)
	}
}

func TestStore_GetByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	got, err := store.GetByID("missing")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestService_CreateLink_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "s1", IntentTerms: "i1"}
	l1, err := svc.CreateLink("t1", cond, "kp1", []string{"ev1"})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	l2, err := svc.CreateLink("t1", cond, "kp1", []string{"ev2"})
	if err != nil {
		t.Fatalf("create link again: %v", err)
	}
	if l1.LinkID != l2.LinkID {
		t.Errorf("expected idempotent link id, got %s vs %s", l1.LinkID, l2.LinkID)
	}
}

func TestService_CreateLink_RejectsRecreateOfDeprecated(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "s1"}
	l1, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(l1.LinkID, StatusDeprecated, "manual_reject", nil); err != nil {
		t.Fatalf("transition to deprecated: %v", err)
	}

	if _, err := svc.CreateLink("t1", cond, "kp1", nil); err == nil {
		t.Error("expected error recreating deprecated link, got nil")
	}
}

func TestService_TransitionLink_LegalPath(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	steps := []string{StatusVerified, StatusWeakened, StatusVerified, StatusWeakened, StatusDeprecated}
	for _, to := range steps {
		updated, err := svc.TransitionLink(l.LinkID, to, "test", []string{"ev1"})
		if err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
		if updated.Status != to {
			t.Errorf("status = %q, want %q", updated.Status, to)
		}
		if !updated.StatusChangedAt.Valid {
			t.Errorf("status_changed_at not set after transition to %s", to)
		}
	}

	results, err := store.ListLearningResultsByObject(ObjectTypeActivationLink, l.LinkID)
	if err != nil {
		t.Fatalf("list learning results: %v", err)
	}
	if len(results) != len(steps) {
		t.Fatalf("expected %d learning results, got %d", len(steps), len(results))
	}
	wantActions := []string{ActionPromote, ActionWeaken, ActionReverify, ActionWeaken, ActionDeprecate}
	for i, r := range results {
		if r.Action != wantActions[i] {
			t.Errorf("result[%d].Action = %q, want %q", i, r.Action, wantActions[i])
		}
		if r.Status != ResultApplied {
			t.Errorf("result[%d].Status = %q, want applied", i, r.Status)
		}
	}
}

func TestService_TransitionLink_RejectsIllegalMoves(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	illegal := []string{StatusCandidate, StatusWeakened}
	for _, to := range illegal {
		if _, err := svc.TransitionLink(l.LinkID, to, "test", nil); err == nil {
			t.Errorf("expected error transitioning candidate -> %s", to)
		}
	}

	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusDeprecated, "test", nil); err == nil {
		t.Error("expected error transitioning verified -> deprecated directly")
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusDeprecated, "test", nil); err != nil {
		t.Fatalf("deprecate from weakened: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err == nil {
		t.Error("expected error transitioning out of deprecated (terminal state)")
	}
}

func TestStore_UpdateStats(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedKPFull(t, db, "kp1")
	link := &ActivationLink{QuestionTerms: "t1", PointID: "kp1"}
	if err := store.InsertLink(link); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := store.UpdateStats(link.LinkID, 2, 0); err != nil {
		t.Fatalf("update stats: %v", err)
	}
	if err := store.UpdateStats(link.LinkID, 1, 3); err != nil {
		t.Fatalf("update stats: %v", err)
	}

	got, err := store.GetByID(link.LinkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AdoptCount != 3 || got.FailCount != 3 {
		t.Errorf("adopt=%d fail=%d, want 3/3", got.AdoptCount, got.FailCount)
	}
}

func TestStore_TouchLastUsed(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedKPFull(t, db, "kp1")
	link := &ActivationLink{QuestionTerms: "t1", PointID: "kp1"}
	if err := store.InsertLink(link); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := store.GetByID(link.LinkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastUsedAt.Valid {
		t.Fatal("expected last_used_at to be null before touch")
	}

	if err := store.TouchLastUsed([]string{link.LinkID}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	got, err = store.GetByID(link.LinkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.LastUsedAt.Valid {
		t.Error("expected last_used_at to be set after touch")
	}
}

func TestStore_ListVerifiedLinksForCurrentKP_FiltersStatusAndLifecycle(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")
	seedKPFull(t, db, "kp2")
	setLifecycle(t, db, "kp2", "superseded")

	candidate, err := svc.CreateLink("t-candidate", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create candidate link: %v", err)
	}
	verifiedCurrent, err := svc.CreateLink("t-verified-current", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(verifiedCurrent.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote: %v", err)
	}
	verifiedStale, err := svc.CreateLink("t-verified-stale", LinkCondition{}, "kp2", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(verifiedStale.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote: %v", err)
	}
	_ = candidate

	links, err := store.ListVerifiedLinksForCurrentKP()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 verified+current link, got %d", len(links))
	}
	if links[0].LinkID != verifiedCurrent.LinkID {
		t.Errorf("unexpected link returned: %+v", links[0])
	}
}

func TestStore_ListLinks_JoinsPointAndUnit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedKPFull(t, db, "kp1")

	link := &ActivationLink{QuestionTerms: "t1", PointID: "kp1"}
	if err := store.InsertLink(link); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := store.ListLinks(ListLinksFilter{})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].PointSummary != "content of kp1" {
		t.Errorf("point_summary = %q", rows[0].PointSummary)
	}
	if rows[0].UnitCenter != "test topic" {
		t.Errorf("unit_center = %q", rows[0].UnitCenter)
	}
}

func TestService_Confirm_ResolvesPendingAndPromotes(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	pending := &LearningResult{
		Action:     ActionPromote,
		ObjectType: ObjectTypeActivationLink,
		ObjectID:   l.LinkID,
		Reason:     "repeated_success threshold met",
		EventIDs:   `["ev1","ev2"]`,
		Status:     ResultPendingConfirm,
	}
	if err := store.InsertLearningResult(pending); err != nil {
		t.Fatalf("insert pending: %v", err)
	}

	updated, err := svc.Confirm(l.LinkID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if updated.Status != StatusVerified {
		t.Errorf("status = %q, want verified", updated.Status)
	}

	results, err := store.ListLearningResultsByObject(ObjectTypeActivationLink, l.LinkID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected pending + new applied result, got %d", len(results))
	}

	var pendingResolved, newApplied bool
	for _, r := range results {
		if r.ResultID == pending.ResultID {
			if r.Status != ResultApplied || !r.ConfirmedBy.Valid || r.ConfirmedBy.String != "manual" {
				t.Errorf("pending result not resolved correctly: %+v", r)
			}
			pendingResolved = true
		} else {
			if r.EventIDs != pending.EventIDs {
				t.Errorf("new result should carry pending's event ids, got %q want %q", r.EventIDs, pending.EventIDs)
			}
			newApplied = true
		}
	}
	if !pendingResolved || !newApplied {
		t.Errorf("expected both pending resolution and new applied result, got %+v", results)
	}
}

func TestService_Confirm_OnlyValidForCandidate(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if _, err := svc.Confirm(l.LinkID); err == nil {
		t.Error("expected error confirming a non-candidate link")
	}
}

func TestService_Reject_MarksDeprecatedAndResolvesPendingAsRejected(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	pending := &LearningResult{
		Action:     ActionPromote,
		ObjectType: ObjectTypeActivationLink,
		ObjectID:   l.LinkID,
		Reason:     "repeated_success threshold met",
		Status:     ResultPendingConfirm,
	}
	if err := store.InsertLearningResult(pending); err != nil {
		t.Fatalf("insert pending: %v", err)
	}

	updated, err := svc.Reject(l.LinkID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if updated.Status != StatusDeprecated {
		t.Errorf("status = %q, want deprecated", updated.Status)
	}

	got, err := store.ListLearningResultsByObject(ObjectTypeActivationLink, l.LinkID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range got {
		if r.ResultID == pending.ResultID && r.Status != ResultRejected {
			t.Errorf("pending result should be rejected, got %q", r.Status)
		}
	}
}
