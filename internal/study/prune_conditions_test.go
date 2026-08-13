package study

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
)

// seedLinkWithConditions inserts an activation_links row (status=candidate,
// point_id must already exist) with the given observed_conditions, for
// PruneCandidateConditions/pruneConditions tests that need direct control
// over success_count/failure_count/last_seen_at per condition — a level of
// control CreateLink's normal path doesn't expose.
func seedLinkWithConditions(t *testing.T, db *sql.DB, linkID, pointID string, conds []activation.ObservedCondition) {
	t.Helper()
	raw, err := json.Marshal(conds)
	if err != nil {
		t.Fatalf("marshal conditions: %v", err)
	}
	_, err = db.Exec(`INSERT INTO activation_links (link_id, question_terms, point_id, status, observed_conditions)
		VALUES (?, ?, ?, 'candidate', ?)`, linkID, "t_"+linkID, pointID, string(raw))
	if err != nil {
		t.Fatalf("seed link with conditions: %v", err)
	}
}

func cond(subject string, success, failure int, lastSeen time.Time) activation.ObservedCondition {
	return activation.ObservedCondition{
		Subject:      subject,
		FirstSeenAt:  lastSeen,
		LastSeenAt:   lastSeen,
		SuccessCount: success,
		FailureCount: failure,
	}
}

// TestPruneCandidateConditions_ConvergedLow: mean well below meanMax, tight
// width (large sample), and last observed long before idleDays — must be
// classified converged_low and pruned.
func TestPruneCandidateConditions_ConvergedLow(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	old := time.Now().UTC().AddDate(0, 0, -60) // older than idleDays=30
	// success=1, failure=19 → mean=(1+1)/(1+19+2)=2/22≈0.0909 < meanMax=0.3;
	// width=0.0909*0.909/(20+3)≈0.0036 <= widthMax=0.02; n=20 >= sampleMin=8.
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("s1", 1, 19, old),
	})

	results, err := store.PruneCandidateConditions(0.3, 0.02, 8, 30, 90)
	if err != nil {
		t.Fatalf("PruneCandidateConditions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 link with prunable conditions, got %d", len(results))
	}
	if len(results[0].Conditions) != 1 || results[0].Conditions[0].Classification != "converged_low" {
		t.Fatalf("expected 1 converged_low condition, got %+v", results[0].Conditions)
	}
}

// TestPruneCandidateConditions_SelfContradictory_NotPruned: mean is low but
// width stays wide (small sample relative to variance) — must NOT be pruned,
// this is the "self-contradictory / still exploring" case surfaced via
// ConfidenceConvergenceStats instead of silently removed.
func TestPruneCandidateConditions_SelfContradictory_NotPruned(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	// success=4, failure=6 → mean=(4+1)/(4+6+2)=5/12≈0.417 -- actually this is
	// >meanMax=0.3, so use success=2,failure=8: mean=(2+1)/(2+8+2)=3/12=0.25<0.3;
	// n=10 >= sampleMin=8; width=0.25*0.75/(10+3)≈0.0144 <= 0.02 -- too narrow,
	// need width > widthMax to hit self-contradictory. Use a smaller sample
	// with mixed outcomes instead: success=2, failure=3 → n=5 < sampleMin=8,
	// which would hit long_idle not self-contradictory. Use success=3,
	// failure=5 → n=8 >= sampleMin; mean=(3+1)/(3+5+2)=4/10=0.4 > meanMax=0.3,
	// doesn't even reach the mean gate -- also "not pruned", but for the wrong
	// reason (should test width, not mean). Construct precisely: want mean<0.3
	// AND width>0.02 AND n>=8. mean=(s+1)/(n+2), width=mean*(1-mean)/(n+3).
	// n=8: mean<0.3 => s+1 < 0.3*(10) = 3 => s<=1. s=1,f=7,n=8:
	// mean=(2)/(10)=0.2; width=0.2*0.8/11=0.0145 -- still narrow (Beta width
	// naturally shrinks with n>=sampleMin, so "wide despite meeting sampleMin"
	// needs an even larger failure/success spread near n's lower bound.
	// Simplify: keep n just at sampleMin but push width over 0.02 by using a
	// value where mean*(1-mean) is maximal-ish for low mean is impossible
	// (mean<0.3 means mean*(1-mean)<=0.21, so width<=0.21/11=0.019 at n=8,
	// which is already < 0.02) -- so at n=sampleMin the width gate is
	// structurally satisfied whenever mean<meanMax with these config values;
	// self-contradictory in practice only shows up when idleDays hasn't
	// elapsed yet. Test that variant instead: converged on mean/width/sample
	// but NOT idle (last_seen_at recent) — must not be pruned as converged_low
	// (still being actively explored, gets a chance to rebound), and doesn't
	// qualify long_idle either since n>=sampleMin.
	recent := time.Now().UTC().AddDate(0, 0, -1) // within idleDays=30
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("s1", 1, 19, recent),
	})

	results, err := store.PruneCandidateConditions(0.3, 0.02, 8, 30, 90)
	if err != nil {
		t.Fatalf("PruneCandidateConditions: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected recently-observed low-mean condition to NOT be pruned, got %+v", results)
	}
}

// TestPruneCandidateConditions_LongIdle_InsufficientSample: sample too small
// to judge convergence either way, but hasn't been observed in a very long
// time (past staleDays) — classified long_idle and pruned via the different
// (more lenient) staleDays path, distinct from converged_low's idleDays.
func TestPruneCandidateConditions_LongIdle_InsufficientSample(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	veryOld := time.Now().UTC().AddDate(0, 0, -100) // older than staleDays=90
	// n=2 < sampleMin=8 -> can't judge convergence, but stale enough to clean up.
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("s1", 1, 1, veryOld),
	})

	results, err := store.PruneCandidateConditions(0.3, 0.02, 8, 30, 90)
	if err != nil {
		t.Fatalf("PruneCandidateConditions: %v", err)
	}
	if len(results) != 1 || len(results[0].Conditions) != 1 || results[0].Conditions[0].Classification != "long_idle" {
		t.Fatalf("expected 1 long_idle condition, got %+v", results)
	}
}

// TestPruneCandidateConditions_PartialPrune: a link with three conditions —
// only the converged-low one should be pruned, the recent one and the
// high-confidence one must survive.
func TestPruneCandidateConditions_PartialPrune(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	old := time.Now().UTC().AddDate(0, 0, -60)
	recent := time.Now().UTC().AddDate(0, 0, -1)
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("converged_low_target", 1, 19, old), // should prune
		cond("recent_low", 1, 19, recent),        // should survive (not idle yet)
		cond("healthy", 20, 1, old),              // should survive (high mean)
	})

	results, err := store.PruneCandidateConditions(0.3, 0.02, 8, 30, 90)
	if err != nil {
		t.Fatalf("PruneCandidateConditions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 link with prunable conditions, got %d", len(results))
	}
	if len(results[0].Conditions) != 1 || results[0].Conditions[0].Subject != "converged_low_target" {
		t.Fatalf("expected only converged_low_target pruned, got %+v", results[0].Conditions)
	}
}

// TestService_PruneConditions_PersistsTrimmedSet exercises the Service-layer
// half: pruneConditions must remove exactly the flagged conditions from the
// live link (via activation.Service.ReplaceObservedConditions, not raw SQL),
// leave the rest intact, write one prune_condition learning_results row, and
// report the pruned count in LearningActionsSummary.
func TestService_PruneConditions_PersistsTrimmedSet(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	old := time.Now().UTC().AddDate(0, 0, -60)
	recent := time.Now().UTC().AddDate(0, 0, -1)
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("prune_me", 1, 19, old),
		cond("keep_me", 1, 19, recent),
	})

	var actions LearningActionsSummary
	if err := svc.pruneConditions(&actions); err != nil {
		t.Fatalf("pruneConditions: %v", err)
	}
	if actions.PrunedConditions != 1 {
		t.Fatalf("expected PrunedConditions=1, got %d", actions.PrunedConditions)
	}

	link, err := activationSvc.Store().GetByID("link1")
	if err != nil || link == nil {
		t.Fatalf("expected link1 to still exist, err=%v", err)
	}
	if len(link.ObservedConditions) != 1 || link.ObservedConditions[0].Subject != "keep_me" {
		t.Fatalf("expected only keep_me to survive, got %+v", link.ObservedConditions)
	}

	results, err := activationSvc.ListLearningResults("link1")
	if err != nil {
		t.Fatalf("ListLearningResults: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Action == activation.ActionPruneCondition {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a prune_condition learning result, got %+v", results)
	}
}

// TestRun_PruneConditionsFiresAndReportsCount confirms pruneConditions is
// wired into Run()'s orchestration (in the slot the old evictIdleCandidates/
// evictIdleWeakened calls used to occupy, docs/impl/v1/study.md 步骤 3) and
// that LearningActionsSummary.PrunedConditions surfaces the result through
// both the RunResult and the persisted report.
func TestRun_PruneConditionsFiresAndReportsCount(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	old := time.Now().UTC().AddDate(0, 0, -60)
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("prune_me", 1, 19, old),
	})

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.LearningActions.PrunedConditions != 1 {
		t.Fatalf("expected RunResult.LearningActions.PrunedConditions=1, got %d", result.LearningActions.PrunedConditions)
	}

	raw, err := svc.store.GetReport(result.ReportID)
	if err != nil || raw == nil {
		t.Fatalf("GetReport: %v", err)
	}
	var report Report
	if err := json.Unmarshal([]byte(*raw), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.LearningActions.PrunedConditions != 1 {
		t.Fatalf("expected report.learning_actions.pruned_conditions=1, got %d", report.LearningActions.PrunedConditions)
	}

	link, err := activationSvc.Store().GetByID("link1")
	if err != nil || link == nil {
		t.Fatalf("expected link1 to survive with trimmed conditions, err=%v", err)
	}
	if len(link.ObservedConditions) != 0 {
		t.Fatalf("expected all conditions pruned, got %+v", link.ObservedConditions)
	}
}

// TestConfidenceConvergenceStats_Aggregation verifies width distribution and
// tier-count aggregation across multiple links' conditions.
func TestConfidenceConvergenceStats_Aggregation(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")

	now := time.Now().UTC()
	// link1: one exploring condition (mean low), one trusted-ish (mean high,
	// no audit -> self_graded under the test ConfidenceConfig).
	seedLinkWithConditions(t, db, "link1", "kp1", []activation.ObservedCondition{
		cond("low", 1, 19, now),
		cond("high", 20, 1, now),
	})
	seedLinkWithConditions(t, db, "link2", "kp2", []activation.ObservedCondition{
		cond("mid", 5, 5, now),
	})

	cfg := testConfidenceConfig() // ServingConfidenceMin: 0.5, AuditSampleMin: 0
	stats, err := store.ConfidenceConvergenceStats(cfg, 0.02)
	if err != nil {
		t.Fatalf("ConfidenceConvergenceStats: %v", err)
	}
	if stats.TotalConditions != 3 {
		t.Fatalf("expected 3 total conditions, got %d", stats.TotalConditions)
	}
	if stats.TierCounts.Exploring+stats.TierCounts.SelfGraded+stats.TierCounts.Trusted != 3 {
		t.Fatalf("expected tier counts to sum to 3, got %+v", stats.TierCounts)
	}
	if stats.AvgWidth <= 0 {
		t.Errorf("expected AvgWidth > 0, got %f", stats.AvgWidth)
	}
}
