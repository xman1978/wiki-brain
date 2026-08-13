package activation

import (
	"time"

	"testing"
)

func TestStore_CreateBundle_And_GetByID(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	b := &ActivationBundle{
		ClusterFingerprint:  "fp1",
		RepresentativeTerms: "topic intent",
		ObservedConditions:  []ObservedCondition{NormalizeObservedCondition("s", "i", "a", "c", "", time.Now())},
		Members:             []BundleMember{{PointID: "p1"}, {PointID: "p2"}, {PointID: "p3"}},
	}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if b.BundleID == "" {
		t.Fatalf("expected bundle_id to be assigned")
	}

	got, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if got == nil {
		t.Fatalf("expected bundle to be found")
	}
	if got.Status != BundleStatusCandidate {
		t.Errorf("expected candidate status, got %s", got.Status)
	}
	if len(got.Members) != 3 {
		t.Errorf("unexpected member size: %v", got.Members)
	}
}

func TestStore_ListMatchableBundles_ExcludesDeprecated(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	live := &ActivationBundle{ClusterFingerprint: "fp-live", Members: []BundleMember{{PointID: "p1"}}}
	dead := &ActivationBundle{ClusterFingerprint: "fp-dead", Members: []BundleMember{{PointID: "p2"}}, Status: BundleStatusCandidate}
	if err := s.CreateBundle(live); err != nil {
		t.Fatalf("create live: %v", err)
	}
	if err := s.CreateBundle(dead); err != nil {
		t.Fatalf("create dead: %v", err)
	}
	if err := s.UpdateBundleStatus(dead.BundleID, BundleStatusDeprecated); err != nil {
		t.Fatalf("set dead to deprecated: %v", err)
	}

	got, err := s.ListMatchableBundles()
	if err != nil {
		t.Fatalf("list matchable bundles: %v", err)
	}
	if len(got) != 1 || got[0].BundleID != live.BundleID {
		t.Errorf("expected only the live bundle, got %+v", got)
	}
}

func TestStore_UpdateBundleMembers_OverwritesSets(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	b := &ActivationBundle{ClusterFingerprint: "fp1", Members: []BundleMember{{PointID: "p1"}}}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	newConds := []ObservedCondition{NormalizeObservedCondition("s2", "i2", "a2", "c2", "", time.Now())}
	newMembers := []BundleMember{{PointID: "p1"}, {PointID: "p2"}, {PointID: "p3"}}
	if err := s.UpdateBundleMembers(b.BundleID, newMembers, newConds); err != nil {
		t.Fatalf("update bundle members: %v", err)
	}
	got, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if len(got.Members) != 3 || len(got.ObservedConditions) != 1 {
		t.Errorf("unexpected state after update: %+v", got)
	}
}

// TestStore_UpdateBundleMembers_DerivesAndPersistsStatus covers the
// 2026-08-13 addition: writing observed_conditions through UpdateBundleMembers
// re-derives and persists the trigger-axis status (docs/impl/v1/activation.md
// 「置信度计算与缓存」applied to Bundle via deriveAndPersistBundleStatus).
func TestStore_UpdateBundleMembers_DerivesAndPersistsStatus(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	s.SetConfidenceConfig(ConfidenceConfig{ServingConfidenceMin: 0.7, AuditSampleMin: 1000})

	b := &ActivationBundle{ClusterFingerprint: "fp1", Members: []BundleMember{{PointID: "p1"}}}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if b.Status != BundleStatusCandidate {
		t.Fatalf("status = %q, want candidate on creation", b.Status)
	}

	confident := NormalizeObservedCondition("s2", "i2", "a2", "c2", "", time.Now())
	confident.SuccessCount = 50
	if err := s.UpdateBundleMembers(b.BundleID, b.Members, []ObservedCondition{confident}); err != nil {
		t.Fatalf("update bundle members: %v", err)
	}
	got, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if got.Status != BundleStatusVerified {
		t.Errorf("status = %q, want verified after boosting condition confidence", got.Status)
	}
}

// TestStore_RecordMemberOutcome_IncrementsOnlyTargetMember covers the new
// member-confidence axis mutator (docs/impl/v1/activation-bundle.md「成员置
// 信度：Bundle 独有的第二根轴」步骤 1's bundle.RecordMemberOutcome): it must
// increment only the targeted point_id's success/failure counter, leave
// every other member untouched, and no-op (not error) on an unknown
// point_id.
func TestStore_RecordMemberOutcome_IncrementsOnlyTargetMember(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	b := &ActivationBundle{
		ClusterFingerprint: "fp1",
		Members:            []BundleMember{{PointID: "p1"}, {PointID: "p2"}},
	}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	if err := s.RecordMemberOutcome(b.BundleID, "p1", true); err != nil {
		t.Fatalf("record member outcome success: %v", err)
	}
	if err := s.RecordMemberOutcome(b.BundleID, "p1", false); err != nil {
		t.Fatalf("record member outcome failure: %v", err)
	}

	got, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	var p1, p2 *BundleMember
	for i := range got.Members {
		switch got.Members[i].PointID {
		case "p1":
			p1 = &got.Members[i]
		case "p2":
			p2 = &got.Members[i]
		}
	}
	if p1 == nil || p1.SuccessCount != 1 || p1.FailureCount != 1 {
		t.Errorf("p1 = %+v, want SuccessCount=1 FailureCount=1", p1)
	}
	if p2 == nil || p2.SuccessCount != 0 || p2.FailureCount != 0 {
		t.Errorf("p2 = %+v, want untouched (0,0)", p2)
	}

	// Unknown point_id: no-op, no error.
	if err := s.RecordMemberOutcome(b.BundleID, "p-unknown", true); err != nil {
		t.Errorf("record member outcome on unknown point_id should no-op, got error: %v", err)
	}
}
