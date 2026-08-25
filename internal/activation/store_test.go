package activation

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return foundation.NewTestDB(t)
}

// testConfidenceCfg is the ConfidenceConfig every activation package test
// uses unless a test specifically wants to probe tier-boundary behavior with
// its own values (docs/impl/v1/activation.md「服务分档」): 0.7 serving
// threshold, a large audit_sample_min so trusted is never reached by
// accident, explore_rate_low=1.0 so an exploring-tier condition always still
// gets a chance to serve in deterministic (non-random-source) tests unless a
// test overrides randFloat.
func testConfidenceCfg() ConfidenceConfig {
	return ConfidenceConfig{
		ServingConfidenceMin:  0.7,
		AuditSampleMin:        1000,
		ExploreRateLow:        1.0,
		ExploreRateSelfGraded: 0,
		ExploreRateTrusted:    0,
	}
}

// newTestService wires a Service with testConfidenceCfg applied — the
// package's default test construction path, replacing the pre-2026-08-13
// bare NewService(store, matcher) call sites so every test gets a
// deterministic confidence configuration instead of the ConfidenceConfig
// zero value (which would make every condition trivially "trusted").
func newTestService(store *Store, matcher *Matcher) *Service {
	svc := NewService(store, matcher)
	svc.SetConfidenceConfig(testConfidenceCfg())
	return svc
}

// verifyLink pushes every one of l's existing observed conditions well past
// the serving threshold (success_count += 50) and persists — the
// replacement for the old TransitionLink(..., StatusVerified, ...) helper
// now that status is derived, not transitioned. A link created with no
// observed conditions (legacy/fallback-path tests) has nothing to boost and
// is returned unchanged — those tests rely on the empty-conditions fallback
// match path, which is tier-exempt by design (see matcher.go), not on
// status=verified.
func verifyLink(t *testing.T, svc *Service, l *ActivationLink) *ActivationLink {
	t.Helper()
	if len(l.ObservedConditions) == 0 {
		return l
	}
	conds := append([]ObservedCondition(nil), l.ObservedConditions...)
	for i := range conds {
		conds[i].SuccessCount += 50
	}
	if err := svc.ReplaceObservedConditions(l.LinkID, conds); err != nil {
		t.Fatalf("verify link (boost conditions): %v", err)
	}
	updated, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link after verify: %v", err)
	}
	return updated
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
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "s1", IntentTerms: []string{"i1"}}
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
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "s1"}
	l1, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := store.UpdateStatus(l1.LinkID, StatusDeprecated); err != nil {
		t.Fatalf("set deprecated: %v", err)
	}

	if _, err := svc.CreateLink("t1", cond, "kp1", nil); err == nil {
		t.Error("expected error recreating deprecated link, got nil")
	}
}

// TestService_DerivedStatus_TracksConditionConfidence replaces the old
// discrete-transition test (TransitionLink was removed 2026-08-13, see
// docs/design/activation-convergence.md): status is now derived from
// observed_conditions confidence, not moved through explicit steps.
// Boosting a condition's success_count past the serving threshold flips the
// link to verified; clearing conditions (Reject) drops it back to candidate;
// deprecating the target KP forces deprecated regardless of confidence.
func TestService_DerivedStatus_TracksConditionConfidence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if l.Status != StatusCandidate {
		t.Fatalf("status = %q, want candidate on creation", l.Status)
	}

	verified := verifyLink(t, svc, l)
	if verified.Status != StatusVerified {
		t.Fatalf("status = %q, want verified after boosting condition confidence", verified.Status)
	}

	rejected, err := svc.Reject(l.LinkID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != StatusCandidate {
		t.Errorf("status = %q, want candidate after reject clears conditions", rejected.Status)
	}
	if len(rejected.ObservedConditions) != 0 {
		t.Errorf("expected observed_conditions cleared, got %+v", rejected.ObservedConditions)
	}

	// Re-accumulate a condition and boost it again, then deprecate via KP
	// lifecycle — deprecated must win over whatever the condition confidence
	// says.
	if err := store.AppendObservedCondition(l.LinkID, NormalizeObservedCondition("住宿", "", "", "", "", "", "", time.Now().UTC()), 50); err != nil {
		t.Fatalf("append condition: %v", err)
	}
	fresh, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	reverified := verifyLink(t, svc, fresh)
	if reverified.Status != StatusVerified {
		t.Fatalf("status = %q, want verified before lifecycle change", reverified.Status)
	}

	setLifecycle(t, db, "kp1", "superseded")
	if err := svc.NotifyPointsLifecycleChanged([]string{"kp1"}); err != nil {
		t.Fatalf("notify lifecycle changed: %v", err)
	}
	deprecated, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if deprecated.Status != StatusDeprecated {
		t.Errorf("status = %q, want deprecated once target KP is no longer current", deprecated.Status)
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

func TestStore_ListLinks_PointIDsBatchFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedKPFull(t, db, "kp1")
	seedKPFull(t, db, "kp2")
	seedKPFull(t, db, "kp3")

	for _, l := range []*ActivationLink{
		{QuestionTerms: "t1", PointID: "kp1"},
		{QuestionTerms: "t2", PointID: "kp2"},
		{QuestionTerms: "t3", PointID: "kp3"},
	} {
		if err := store.InsertLink(l); err != nil {
			t.Fatalf("insert link for %s: %v", l.PointID, err)
		}
	}

	rows, err := store.ListLinks(ListLinksFilter{PointIDs: []string{"kp1", "kp3"}})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (kp1+kp3, not kp2), got %d", len(rows))
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.PointID] = true
	}
	if !got["kp1"] || !got["kp3"] || got["kp2"] {
		t.Errorf("unexpected point_ids in result: %+v", got)
	}
}

func TestStore_ListLinks_PointIDsDefaultsToBulkLimit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	var pointIDs []string
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("kp%d", i)
		seedKPFull(t, db, id)
		pointIDs = append(pointIDs, id)
		if err := store.InsertLink(&ActivationLink{QuestionTerms: fmt.Sprintf("t%d", i), PointID: id}); err != nil {
			t.Fatalf("insert link %d: %v", i, err)
		}
	}

	// The default single-point/status browse limit is 50 — with 60 points
	// each getting one link, a caller that forgot the PointIDs bulk default
	// would silently lose 10 rows off a real concept-page modal.
	rows, err := store.ListLinks(ListLinksFilter{PointIDs: pointIDs})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(rows) != 60 {
		t.Fatalf("expected all 60 rows, got %d (bulk default limit not applied)", len(rows))
	}
}

// TestService_Reject_ValidForAnyStatus_ClearsConditionsAndWritesPruneRecord
// covers the 2026-08-13 rewrite (docs/impl/v1/activation.md 步骤 3): Reject
// is valid for ANY status (not just candidate — a link that's already
// verified may still turn out to rest on untrustworthy evidence), clears
// ObservedConditions, and writes a prune_condition learning_results row
// instead of the removed promote/deprecate transition vocabulary.
func TestService_Reject_ValidForAnyStatus_ClearsConditionsAndWritesPruneRecord(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	updated, err := svc.Reject(l.LinkID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if updated.Status != StatusCandidate {
		t.Errorf("status = %q, want candidate (empty conditions default landing point)", updated.Status)
	}
	if len(updated.ObservedConditions) != 0 {
		t.Errorf("expected observed_conditions cleared, got %+v", updated.ObservedConditions)
	}

	got, err := store.ListLearningResultsByObject(ObjectTypeActivationLink, l.LinkID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range got {
		if r.Action == ActionPruneCondition && r.Reason == "manual_reject" && r.Status == ResultApplied {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a prune_condition/manual_reject/applied learning_results row, got %+v", got)
	}
}

// TestRecordOutcome_InvariantsAndImmediateStatus covers the plan's required
// invariant tests: audited_* increments only ever ride along with
// success_count/failure_count (never independently), and status reflects
// the new counts without needing Match() to run again.
func TestRecordOutcome_InvariantsAndImmediateStatus(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	cond := l.ObservedConditions[0]

	for i := 0; i < 50; i++ {
		if err := svc.RecordOutcome(l.LinkID, cond.Subject, cond.Intent, cond.Audience, cond.Constraint, true, ""); err != nil {
			t.Fatalf("record outcome: %v", err)
		}
	}
	updated, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if updated.Status != StatusVerified {
		t.Fatalf("status = %q, want verified immediately after RecordOutcome (no Match() needed)", updated.Status)
	}
	if updated.ObservedConditions[0].SuccessCount != 51 {
		t.Errorf("success_count = %d, want 51 (1 from EffectiveConditions seed + 50 RecordOutcome calls)", updated.ObservedConditions[0].SuccessCount)
	}

	if err := svc.RecordAuditOutcome(l.LinkID, cond.Subject, cond.Intent, cond.Audience, cond.Constraint, true); err != nil {
		t.Fatalf("record audit outcome (agree): %v", err)
	}
	if err := svc.RecordAuditOutcome(l.LinkID, cond.Subject, cond.Intent, cond.Audience, cond.Constraint, false); err != nil {
		t.Fatalf("record audit outcome (disagree): %v", err)
	}
	final, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	fc := final.ObservedConditions[0]
	if fc.AuditedSuccessCount != 1 || fc.AuditedFailureCount != 1 {
		t.Fatalf("audited counts = %d/%d, want 1/1", fc.AuditedSuccessCount, fc.AuditedFailureCount)
	}
	if fc.AuditedSuccessCount > fc.SuccessCount {
		t.Errorf("invariant broken: audited_success_count (%d) > success_count (%d)", fc.AuditedSuccessCount, fc.SuccessCount)
	}
	if fc.AuditedFailureCount > fc.FailureCount {
		t.Errorf("invariant broken: audited_failure_count (%d) > failure_count (%d)", fc.AuditedFailureCount, fc.FailureCount)
	}
}

// TestRecordOutcome_NoMatchingCondition_WarnsWithoutError covers "定位不到
// 匹配条件时...不报错、记录 warn" (docs/impl/v1/activation.md 步骤 1).
func TestRecordOutcome_NoMatchingCondition_WarnsWithoutError(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	if err := svc.RecordOutcome(l.LinkID, "从未出现过的主体", "", "", "", true, ""); err != nil {
		t.Fatalf("expected no error on missing condition, got %v", err)
	}
	unchanged, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if unchanged.ObservedConditions[0].SuccessCount != l.ObservedConditions[0].SuccessCount {
		t.Errorf("expected condition untouched on no-match, got %+v", unchanged.ObservedConditions[0])
	}
}
