package study

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
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

func TestScanActivationBundles_ClusterMeetsThreshold_CreatesBundle(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	cfg.BundleClusterMinQuestions = 3
	cfg.BundleClusterMinDaysActive = 3
	cfg.BundleCoreRatioMin = 0.5
	cfg.BundleCoreSizeMax = 8
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	// 3 distinct questions, 3 distinct days, all citing kp1+kp2 together —
	// clears both cluster gates and both points end up core (ratio 1.0).
	seedBundleTrace(t, db, "tr1", "q1 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q2 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 1)
	seedBundleTrace(t, db, "tr3", "q3 关于绩效考核", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 2)

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
	cfg := testConfig()
	cfg.BundleClusterMinQuestions = 5
	cfg.BundleClusterMinDaysActive = 5
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

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

func TestScanActivationBundles_RepeatedSignal_MatchAbsorbsInsteadOfDuplicating(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	cfg.BundleClusterMinQuestions = 3
	cfg.BundleClusterMinDaysActive = 3
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content1")
	seedKP(t, db, "kp2", "ku1", "src1", "content2")

	seedBundleTrace(t, db, "tr1", "q1", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 0)
	seedBundleTrace(t, db, "tr2", "q2", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 1)
	seedBundleTrace(t, db, "tr3", "q3", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 2)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	bundles, err := activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil || len(bundles) != 1 {
		t.Fatalf("expected 1 bundle after first scan, got %d (err=%v)", len(bundles), err)
	}

	// Same exact four-tuple asked again — round-1 exact Match should absorb
	// it into the existing bundle rather than spawning a second one.
	seedBundleTrace(t, db, "tr4", "q4", "绩效考核", "怎么算", "", "", []string{"kp1", "kp2"}, 3)
	if err := svc.scanActivationBundles(); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	bundles, err = activationSvc.Store().ListBundlesByStatus(nil, 50, 0)
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected still 1 bundle (absorbed, not duplicated), got %d: %+v", len(bundles), bundles)
	}
}

func TestWeakenBundlesWithExpiredCoreMembers_CoreExpired_Weakens(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

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
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

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
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, 0)

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
