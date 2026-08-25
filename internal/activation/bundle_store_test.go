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
		ObservedConditions:  []ObservedCondition{NormalizeObservedCondition("s", "i", "a", "c", "", "", "", time.Now())},
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

	got, err := s.ListMatchableBundles(nil)
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
	newConds := []ObservedCondition{NormalizeObservedCondition("s2", "i2", "a2", "c2", "", "", "", time.Now())}
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

	confident := NormalizeObservedCondition("s2", "i2", "a2", "c2", "", "", "", time.Now())
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

// TestStore_RecordBundleOutcome_IncrementsMatchedCondition covers the
// trigger-axis mutator added 2026-08-20 (docs/impl/v1/activation-bundle.md
// 「验证」阶段 2 接线): success/failure land on the condition matching the
// exact quadruple, a non-matching quadruple is a no-op (matched=false), and
// the derived status is re-evaluated after the write.
func TestStore_RecordBundleOutcome_IncrementsMatchedCondition(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	s.SetConfidenceConfig(ConfidenceConfig{ServingConfidenceMin: 0.6, AuditSampleMin: 1000})

	cond := NormalizeObservedCondition("退休金", "计算", "普通用户", "", "", "", "", time.Now())
	b := &ActivationBundle{
		ClusterFingerprint: "fp1",
		ObservedConditions: []ObservedCondition{cond},
		Members:            []BundleMember{{PointID: "p1"}},
	}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	for i := 0; i < 5; i++ {
		matched, _, err := s.RecordBundleOutcome(b.BundleID, "退休金", "计算", "普通用户", "", true)
		if err != nil {
			t.Fatalf("record bundle outcome: %v", err)
		}
		if !matched {
			t.Fatalf("expected condition to match on iteration %d", i)
		}
	}
	matched, _, err := s.RecordBundleOutcome(b.BundleID, "退休金", "计算", "普通用户", "", false)
	if err != nil {
		t.Fatalf("record bundle outcome failure: %v", err)
	}
	if !matched {
		t.Fatalf("expected condition to match")
	}

	got, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if len(got.ObservedConditions) != 1 {
		t.Fatalf("expected exactly one condition, got %d", len(got.ObservedConditions))
	}
	c := got.ObservedConditions[0]
	// NormalizeObservedCondition seeds SuccessCount=1 at creation, plus 5
	// RecordBundleOutcome successes = 6.
	if c.SuccessCount != 6 || c.FailureCount != 1 {
		t.Errorf("condition = %+v, want SuccessCount=6 FailureCount=1", c)
	}
	if got.Status != BundleStatusVerified {
		t.Errorf("status = %q, want verified after 5 successes / 1 failure at ServingConfidenceMin=0.6", got.Status)
	}

	// Non-matching quadruple: no-op, no error.
	matched, _, err = s.RecordBundleOutcome(b.BundleID, "无关问题", "查询", "普通用户", "", true)
	if err != nil {
		t.Fatalf("record bundle outcome on non-matching quadruple: %v", err)
	}
	if matched {
		t.Errorf("expected no match for an unrelated quadruple")
	}
}

// TestStore_RefreshBundleMembers_PreservesLiveOutcomeCounts is the regression
// test for the 2026-08-20 fix: Study's periodic rebuild (bundle_scan.go)
// recomputes members/conditions purely from historical co-occurrence and
// used to call UpdateBundleMembers directly, unconditionally overwriting
// live-accumulated RecordBundleOutcome/RecordMemberOutcome counts every tick.
// RefreshBundleMembers must merge instead: an existing member/condition keeps
// its stored counts, only a brand-new candidate seeds fresh values.
func TestStore_RefreshBundleMembers_PreservesLiveOutcomeCounts(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	s.SetConfidenceConfig(ConfidenceConfig{ServingConfidenceMin: 0.6, AuditSampleMin: 1000})

	cond := NormalizeObservedCondition("退休金", "计算", "普通用户", "", "", "", "", time.Now())
	b := &ActivationBundle{
		ClusterFingerprint: "fp1",
		ObservedConditions: []ObservedCondition{cond},
		Members:            []BundleMember{{PointID: "p1"}},
	}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	// Simulate live traffic: p1 and the trigger condition both accumulate
	// real serving outcomes between two Study ticks.
	for i := 0; i < 4; i++ {
		if _, _, err := s.RecordBundleOutcome(b.BundleID, "退休金", "计算", "普通用户", "", true); err != nil {
			t.Fatalf("record bundle outcome: %v", err)
		}
	}
	if err := s.RecordMemberOutcome(b.BundleID, "p1", true); err != nil {
		t.Fatalf("record member outcome: %v", err)
	}
	if err := s.RecordMemberOutcome(b.BundleID, "p1", true); err != nil {
		t.Fatalf("record member outcome: %v", err)
	}

	before, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle before refresh: %v", err)
	}
	// NormalizeObservedCondition seeds SuccessCount=1 at creation, plus 4
	// RecordBundleOutcome successes = 5.
	if before.ObservedConditions[0].SuccessCount != 5 {
		t.Fatalf("setup invariant broken: SuccessCount = %d, want 5", before.ObservedConditions[0].SuccessCount)
	}
	var p1Before *BundleMember
	for i := range before.Members {
		if before.Members[i].PointID == "p1" {
			p1Before = &before.Members[i]
		}
	}
	if p1Before == nil || p1Before.SuccessCount != 2 {
		t.Fatalf("setup invariant broken: p1 = %+v, want SuccessCount=2", p1Before)
	}

	// Study's rebuild recomputes fresh candidate members/conditions from
	// history — a different appearance ratio for p1, plus a brand-new point
	// p2 discovered this tick, and the same canonical trigger condition
	// re-derived (fresh SuccessCount from that rebuild's own trace count,
	// deliberately different from the live-accumulated 5, to prove it gets
	// ignored).
	rebuiltCond := NormalizeObservedCondition("退休金", "计算", "普通用户", "", "", "", "", time.Now())
	rebuiltCond.SuccessCount = 30
	rebuiltMembers := []BundleMember{
		{PointID: "p1", SuccessCount: 9, FailureCount: 1, LastSeenAt: time.Now()},
		{PointID: "p2", SuccessCount: 3, FailureCount: 0, LastSeenAt: time.Now()},
	}
	if err := s.RefreshBundleMembers(b.BundleID, rebuiltMembers, []ObservedCondition{rebuiltCond}); err != nil {
		t.Fatalf("refresh bundle members: %v", err)
	}

	after, err := s.GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle after refresh: %v", err)
	}
	if len(after.ObservedConditions) != 1 {
		t.Fatalf("expected exactly one condition after refresh, got %d", len(after.ObservedConditions))
	}
	if after.ObservedConditions[0].SuccessCount != 5 {
		t.Errorf("trigger condition SuccessCount = %d, want live-accumulated 5 preserved (not rebuild's 30)",
			after.ObservedConditions[0].SuccessCount)
	}

	var p1After, p2After *BundleMember
	for i := range after.Members {
		switch after.Members[i].PointID {
		case "p1":
			p1After = &after.Members[i]
		case "p2":
			p2After = &after.Members[i]
		}
	}
	if p1After == nil || p1After.SuccessCount != 2 || p1After.FailureCount != 0 {
		t.Errorf("p1 = %+v, want live-accumulated SuccessCount=2 FailureCount=0 preserved (not rebuild's 9/1)", p1After)
	}
	if p2After == nil || p2After.SuccessCount != 3 || p2After.FailureCount != 0 {
		t.Errorf("p2 (brand-new this tick) = %+v, want the rebuild's seed values 3/0", p2After)
	}
}
