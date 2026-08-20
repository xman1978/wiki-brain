package study

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

// seedBundleTrace inserts a confident multi-point trace with full control
// over the four-tuple and created_at (bundle_scan.go's clustering keys off
// both, unlike seedTrace which hardcodes intent/audience/constraint empty).
func seedBundleTrace(t *testing.T, db *sql.DB, traceID, question, subject, intent, audience, constraint string, pointIDs []string, daysAgo int) {
	t.Helper()
	answerID := "ans-" + traceID
	if _, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name) VALUES (?, ?, 'a', 'short', 'v1', 'test')`, answerID, question); err != nil {
		t.Fatalf("seed answer: %v", err)
	}
	pidsJSON, _ := json.Marshal(pointIDs)
	_, err := db.Exec(`INSERT INTO traces
		(trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids, subject, intent, audience, constraint_text, created_at)
		VALUES (?, ?, ?, ?, ?, 'confident', 'short', ?, ?, ?, ?, ?, datetime('now', ?))`,
		traceID, answerID, question, "hash_"+traceID, subject, string(pidsJSON), subject, intent, audience, constraint,
		"-"+strconv.Itoa(daysAgo)+" days")
	if err != nil {
		t.Fatalf("seed bundle trace: %v", err)
	}
}

// bundleTestConfig sets create_confidence_min/create_width_max loose enough
// that a handful of confident co-citations reliably cross the creation gate
// — the 2026-08-20 redesign reuses these two fields for Bundle generation
// (no separate bundle_* threshold config), same as ActivationLink.
func bundleTestConfig() config.StudyConfig {
	cfg := testConfig()
	cfg.CreateConfidenceMin = 0.55
	cfg.CreateWidthMax = 0.03
	return cfg
}

func TestScanActivationBundles_CrossesCreationGate_CreatesBundle(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, bundleTestConfig(), activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	// 3 confident co-citations of kp1+kp2 under the exact same four-tuple —
	// 2026-08-20 redesign: repeated identical wording accumulates evidence
	// correctly (the old distinct_question_count gate would have ignored
	// repeats entirely, see the regression test below).
	seedBundleTrace(t, db, "tr1", "q 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr3", "q 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)

	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("scanActivationBundles: %v", err)
	}

	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle, got %d: %+v", len(bundles), bundles)
	}
	if len(bundles[0].Members) != 2 {
		t.Errorf("expected both points to be members, got %+v", bundles[0].Members)
	}
	if bundles[0].Status != activation.BundleStatusCandidate {
		t.Errorf("expected new bundle to stay candidate (阶段 1 无 bundle_success 事件), got %s", bundles[0].Status)
	}
}

func TestScanActivationBundles_BelowThreshold_NoBundleCreated(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, bundleTestConfig(), activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	// A single co-citation isn't enough evidence to cross create_width_max.
	seedBundleTrace(t, db, "tr1", "q1", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)

	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("scanActivationBundles: %v", err)
	}

	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected no bundle below threshold, got %d", len(bundles))
	}
}

func TestScanActivationBundles_RepeatedScan_RefreshesInsteadOfDuplicating(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, bundleTestConfig(), activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	seedBundleTrace(t, db, "tr1", "q1", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q2", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr3", "q3", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil || len(bundles) != 1 {
		t.Fatalf("expected 1 bundle after first scan, got %d (err=%v)", len(bundles), err)
	}

	// Same canonical tuple asked again — second scan should refresh the
	// existing bundle (findMatchingCondition finds the covering condition)
	// rather than spawning a second one.
	seedBundleTrace(t, db, "tr4", "q4", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	bundles, err = activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected still 1 bundle (refreshed, not duplicated), got %d: %+v", len(bundles), bundles)
	}
}

// TestScanActivationBundles_RepeatedScan_PreservesLiveOutcomeCounts is the
// regression test for the 2026-08-20「验证」fix: between two Study ticks a
// Bundle can accumulate live RecordBundleOutcome/RecordMemberOutcome counts
// from real serving traffic — the second scan's rebuild must not reset them
// back to whatever the historical co-occurrence snapshot alone would say.
func TestScanActivationBundles_RepeatedScan_PreservesLiveOutcomeCounts(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, bundleTestConfig(), activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	seedBundleTrace(t, db, "tr1", "q1", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q2", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr3", "q3", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil || len(bundles) != 1 {
		t.Fatalf("expected 1 bundle after first scan, got %d (err=%v)", len(bundles), err)
	}
	bundleID := bundles[0].BundleID

	// Simulate real serving traffic landing between the two Study ticks:
	// the trigger condition and member kp1 both accumulate live outcomes.
	for i := 0; i < 6; i++ {
		if _, _, err := activationSvc.Store().RecordBundleOutcome(bundleID, "绩效考核", "怎么算", "", "", true); err != nil {
			t.Fatalf("record bundle outcome: %v", err)
		}
	}
	if err := activationSvc.Store().RecordMemberOutcome(bundleID, "kp1", true); err != nil {
		t.Fatalf("record member outcome: %v", err)
	}
	if err := activationSvc.Store().RecordMemberOutcome(bundleID, "kp1", true); err != nil {
		t.Fatalf("record member outcome: %v", err)
	}
	before, err := activationSvc.Store().GetBundleByID(bundleID)
	if err != nil {
		t.Fatalf("get bundle before second scan: %v", err)
	}
	triggerBefore := before.ObservedConditions[0].SuccessCount
	var kp1Before int
	for _, m := range before.Members {
		if m.PointID == "kp1" {
			kp1Before = m.SuccessCount
		}
	}

	// A new confident trace for the same canonical tuple arrives — this
	// drives a second scan that refreshes (not recreates) the bundle.
	seedBundleTrace(t, db, "tr4", "q4", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("second scan: %v", err)
	}

	after, err := activationSvc.Store().GetBundleByID(bundleID)
	if err != nil {
		t.Fatalf("get bundle after second scan: %v", err)
	}
	if len(after.ObservedConditions) != 1 {
		t.Fatalf("expected exactly one condition after second scan, got %d", len(after.ObservedConditions))
	}
	if after.ObservedConditions[0].SuccessCount != triggerBefore {
		t.Errorf("trigger condition SuccessCount = %d, want live-accumulated %d preserved across the rebuild",
			after.ObservedConditions[0].SuccessCount, triggerBefore)
	}
	var kp1After int
	found := false
	for _, m := range after.Members {
		if m.PointID == "kp1" {
			kp1After = m.SuccessCount
			found = true
		}
	}
	if !found {
		t.Fatalf("expected kp1 to still be a member after second scan")
	}
	if kp1After != kp1Before {
		t.Errorf("kp1 SuccessCount = %d, want live-accumulated %d preserved across the rebuild", kp1After, kp1Before)
	}
}

// TestScanActivationBundles_DifferentMemberCounts_MergeUnderSameCanonicalTuple
// regression-tests the 2026-08-20 redesign's core fix: {p1,p2} and
// {p1,p2,p3} co-citations under the SAME canonical question merge into one
// Bundle (member roster = union), rather than forking into two Bundles by
// member-set identity.
func TestScanActivationBundles_DifferentMemberCounts_MergeUnderSameCanonicalTuple(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, bundleTestConfig(), activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")
	seedKP(t, db, "kp3", "ku1", "src1", "content3")

	seedBundleTrace(t, db, "tr1", "q1", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q2", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2", "kp3"}, 0)
	seedBundleTrace(t, db, "tr3", "q3", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)

	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("scanActivationBundles: %v", err)
	}

	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle (same canonical tuple merges member sets), got %d: %+v", len(bundles), bundles)
	}
	if len(bundles[0].Members) != 3 {
		t.Fatalf("expected member roster to be the union {kp1,kp2,kp3}, got %+v", bundles[0].Members)
	}
}

func TestWeakenBundlesWithExpiredCoreMembers_CoreExpired_Weakens(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	b := &activation.ActivationBundle{ClusterFingerprint: "fp1", Members: []activation.BundleMember{
		{PointID: "kp1", SuccessCount: 10}, {PointID: "kp2", SuccessCount: 10},
	}, Status: activation.BundleStatusCandidate}
	if err := activationSvc.Store().CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(b.BundleID, activation.BundleStatusVerified); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}

	if _, err := db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp1'`); err != nil {
		t.Fatalf("expire kp1: %v", err)
	}

	if err := svc.weakenBundlesWithExpiredCoreMembers(); err != nil {
		t.Fatalf("weakenBundlesWithExpiredCoreMembers: %v", err)
	}

	got, err := activationSvc.Store().GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if got.Status != activation.BundleStatusDeprecated {
		t.Errorf("expected deprecated after core member lifecycle expiry, got %s", got.Status)
	}
}

func TestWeakenBundlesWithExpiredCoreMembers_FringeExpired_NoStateChange(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	// kp1 is core (high success_count, tier self_graded/trusted); kp2 is
	// fringe (mostly failures, tier exploring, see conditionTier).
	b := &activation.ActivationBundle{ClusterFingerprint: "fp1", Members: []activation.BundleMember{
		{PointID: "kp1", SuccessCount: 10}, {PointID: "kp2", FailureCount: 10},
	}, Status: activation.BundleStatusCandidate}
	if err := activationSvc.Store().CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := activationSvc.Store().UpdateBundleStatus(b.BundleID, activation.BundleStatusVerified); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}

	// Only the fringe member kp2 expires — core member kp1 stays current.
	if _, err := db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp2'`); err != nil {
		t.Fatalf("expire kp2: %v", err)
	}

	if err := svc.weakenBundlesWithExpiredCoreMembers(); err != nil {
		t.Fatalf("weakenBundlesWithExpiredCoreMembers: %v", err)
	}

	got, err := activationSvc.Store().GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if got.Status != activation.BundleStatusVerified {
		t.Errorf("expected still verified (only fringe expired), got %s", got.Status)
	}
}

func TestWeakenBundlesWithExpiredCoreMembers_CandidateBundle_NoStateChange(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0, false)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")

	// Never promoted to verified — stays candidate.
	b := &activation.ActivationBundle{ClusterFingerprint: "fp1", Members: []activation.BundleMember{{PointID: "kp1", SuccessCount: 10}}, Status: activation.BundleStatusCandidate}
	if err := activationSvc.Store().CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	if _, err := db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp1'`); err != nil {
		t.Fatalf("expire kp1: %v", err)
	}

	if err := svc.weakenBundlesWithExpiredCoreMembers(); err != nil {
		t.Fatalf("weakenBundlesWithExpiredCoreMembers: %v", err)
	}

	got, err := activationSvc.Store().GetBundleByID(b.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if got.Status != activation.BundleStatusCandidate {
		t.Errorf("expected still candidate (only verified bundles are subject to lifecycle-driven weaken), got %s", got.Status)
	}
}
