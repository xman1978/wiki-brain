package study

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
)

func seedActivationSuccessEvent(t *testing.T, db *sql.DB, eventID, traceID, linkID, pointID string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"link_id": linkID, "point_id": pointID, "question_terms": "t", "match_score": 0.9, "cited_fact_ids": []string{"f1"},
	})
	seedLearningEvent(t, db, eventID, traceID, "activation_success", string(payload))
}

func seedActivationFailureEvent(t *testing.T, db *sql.DB, eventID, traceID, linkID, pointID string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"link_id": linkID, "point_id": pointID, "question_terms": "t", "match_score": 0.7, "reason": "not_cited",
	})
	seedLearningEvent(t, db, eventID, traceID, "activation_failure", string(payload))
}

func seedUserCorrectionEvent(t *testing.T, db *sql.DB, eventID, traceID string, linkIDs []string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"feedback_content": "wrong", "feedback_type": "negative", "link_ids": linkIDs,
	})
	seedLearningEvent(t, db, eventID, traceID, "user_correction", string(payload))
}

func seedActivationGapEvent(t *testing.T, db *sql.DB, eventID, traceID, questionTerms string, directPointIDs []string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"question_terms": questionTerms, "direct_point_ids": directPointIDs,
	})
	seedLearningEvent(t, db, eventID, traceID, "activation_gap", string(payload))
}

func backdateLinkCreatedAt(t *testing.T, db *sql.DB, linkID string, days int) {
	t.Helper()
	_, err := db.Exec(`UPDATE activation_links SET created_at = datetime('now', '-' || ? || ' days') WHERE link_id = ?`, days, linkID)
	if err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}
}

func backdateLinkStatusChangedAt(t *testing.T, db *sql.DB, linkID string, days int) {
	t.Helper()
	_, err := db.Exec(`UPDATE activation_links SET status_changed_at = datetime('now', '-' || ? || ' days') WHERE link_id = ?`, days, linkID)
	if err != nil {
		t.Fatalf("backdate status_changed_at: %v", err)
	}
}

func setupStudyWithActivation(t *testing.T) (*Service, *Store, *activation.Service, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfig(), activationSvc, nil, 0)
	return svc, store, activationSvc, db
}

func TestCreateCandidates_SourceA_Cooccurrence(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})

	// Step 1 scan first so link_candidates has the row.
	if _, err := svc.store.ScanCandidates(5, 0.6, 200); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var actions LearningActionsSummary
	if err := svc.createCandidates(&actions); err != nil {
		t.Fatalf("createCandidates: %v", err)
	}
	if actions.CreatedCandidates != 1 {
		t.Fatalf("expected 1 created candidate, got %d", actions.CreatedCandidates)
	}

	link, err := activationSvc.Store().GetByQuestionAndPoint("并发 问题", "kp1")
	if err != nil || link == nil {
		t.Fatalf("expected link to exist, err=%v", err)
	}
	if link.Status != activation.StatusCandidate {
		t.Errorf("status = %q, want candidate", link.Status)
	}

	results, err := activationSvc.ListLearningResults(link.LinkID)
	if err != nil || len(results) != 1 || results[0].Action != activation.ActionCreateCandidate {
		t.Fatalf("expected 1 create_candidate learning result, got %+v (err=%v)", results, err)
	}
}

func TestCreateCandidates_Idempotent(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})
	svc.store.ScanCandidates(5, 0.6, 200)

	var actions1, actions2 LearningActionsSummary
	svc.createCandidates(&actions1)
	svc.createCandidates(&actions2)

	if actions1.CreatedCandidates != 1 {
		t.Fatalf("first run: expected 1 created, got %d", actions1.CreatedCandidates)
	}
	if actions2.CreatedCandidates != 0 {
		t.Fatalf("second run: expected 0 created (idempotent), got %d", actions2.CreatedCandidates)
	}

	link, _ := activationSvc.Store().GetByQuestionAndPoint("并发 问题", "kp1")
	results, _ := activationSvc.ListLearningResults(link.LinkID)
	if len(results) != 1 {
		t.Errorf("expected exactly 1 create_candidate result (no duplicate), got %d", len(results))
	}
}

func TestCreateCandidates_RejectsRecreateOfDeprecated(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})
	svc.store.ScanCandidates(5, 0.6, 200)

	var actions LearningActionsSummary
	svc.createCandidates(&actions)
	link, _ := activationSvc.Store().GetByQuestionAndPoint("并发 问题", "kp1")
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusDeprecated, "manual_reject", nil); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	var actions2 LearningActionsSummary
	if err := svc.createCandidates(&actions2); err != nil {
		t.Fatalf("createCandidates 2nd: %v", err)
	}
	if actions2.CreatedCandidates != 0 {
		t.Errorf("expected no recreation of deprecated link, got %d created", actions2.CreatedCandidates)
	}
}

func TestCreateCandidates_SourceB_ActivationGap(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "分布式 系统", "kp1", 2, 2)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "分布式系统是什么", "分布式 系统", "confident", "short", []string{"kp1"})
	seedActivationGapEvent(t, db, "evt-gap1", "tr1", "分布式 系统", []string{"kp1"})

	var actions LearningActionsSummary
	if err := svc.createCandidates(&actions); err != nil {
		t.Fatalf("createCandidates: %v", err)
	}
	if actions.CreatedCandidates != 1 {
		t.Fatalf("expected 1 created candidate from activation_gap, got %d", actions.CreatedCandidates)
	}

	link, err := activationSvc.Store().GetByQuestionAndPoint("分布式 系统", "kp1")
	if err != nil || link == nil {
		t.Fatalf("expected link to exist, err=%v", err)
	}

	var processed int
	db.QueryRow(`SELECT processed FROM learning_events WHERE event_id = 'evt-gap1'`).Scan(&processed)
	if processed != 1 {
		t.Error("expected activation_gap event marked processed")
	}
}

func TestCreateCandidates_SourceB_BelowThreshold_NoLink(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "分布式 系统", "kp1", 1, 1) // only 1 confident occurrence
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "分布式系统是什么", "分布式 系统", "confident", "short", []string{"kp1"})
	seedActivationGapEvent(t, db, "evt-gap1", "tr1", "分布式 系统", []string{"kp1"})

	var actions LearningActionsSummary
	if err := svc.createCandidates(&actions); err != nil {
		t.Fatalf("createCandidates: %v", err)
	}
	if actions.CreatedCandidates != 0 {
		t.Errorf("expected no candidate below reoccurrence threshold, got %d", actions.CreatedCandidates)
	}

	link, _ := activationSvc.Store().GetByQuestionAndPoint("分布式 系统", "kp1")
	if link != nil {
		t.Error("expected no link created")
	}
}

func TestProcessLinkSignals_PromoteToPendingConfirm(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	for i, trID := range []string{"tr1", "tr2", "tr3"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "confident", "short", []string{"kp1"})
		seedActivationSuccessEvent(t, db, "evt-s"+trID, trID, link.LinkID, "kp1")
		_ = i
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}

	if actions.PendingPromotions != 1 {
		t.Fatalf("expected 1 pending promotion, got %d (actions=%+v)", actions.PendingPromotions, actions)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusCandidate {
		t.Errorf("status should stay candidate pending confirm, got %q", updated.Status)
	}
	if updated.AdoptCount != 3 {
		t.Errorf("adopt_count = %d, want 3", updated.AdoptCount)
	}

	pending, err := activationSvc.Store().FindPendingPromote(link.LinkID)
	if err != nil || pending == nil {
		t.Fatalf("expected pending promote learning_result, err=%v", err)
	}

	// Re-running should not create a second pending_confirm.
	var actions2 LearningActionsSummary
	// Need fresh unprocessed events to re-trigger the candidate branch; without them, batch is empty.
	seedAnswer(t, db, "ans-tr4")
	seedTrace(t, db, "tr4", "ans-tr4", "q", "t1", "confident", "short", []string{"kp1"})
	seedActivationSuccessEvent(t, db, "evt-s-tr4", "tr4", link.LinkID, "kp1")
	if err := svc.processLinkSignals(&actions2); err != nil {
		t.Fatalf("processLinkSignals 2nd: %v", err)
	}
	if actions2.PendingPromotions != 0 {
		t.Errorf("expected no duplicate pending promotion, got %d", actions2.PendingPromotions)
	}
}

func TestProcessLinkSignals_AutoPromote(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	cfg := testConfig()
	cfg.AutoPromote = true
	svc := NewService(store, cfg, activationSvc, nil, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	for _, trID := range []string{"tr1", "tr2", "tr3"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "confident", "short", []string{"kp1"})
		seedActivationSuccessEvent(t, db, "evt-s"+trID, trID, link.LinkID, "kp1")
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.Promoted != 1 {
		t.Fatalf("expected 1 auto-promotion, got %d", actions.Promoted)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusVerified {
		t.Errorf("status = %q, want verified", updated.Status)
	}
}

func TestProcessLinkSignals_Weaken(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}

	for i, trID := range []string{"tr1", "tr2", "tr3"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "gap", "short", []string{})
		seedActivationFailureEvent(t, db, "evt-f"+trID, trID, link.LinkID, "kp1")
		_ = i
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.Weakened != 1 {
		t.Fatalf("expected 1 weaken, got %d (actions=%+v)", actions.Weakened, actions)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusWeakened {
		t.Errorf("status = %q, want weakened", updated.Status)
	}
	if updated.FailCount != 3 {
		t.Errorf("fail_count = %d, want 3", updated.FailCount)
	}
}

func TestProcessLinkSignals_Reverify(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}

	for i, trID := range []string{"tr1", "tr2"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "confident", "short", []string{"kp1"})
		seedActivationSuccessEvent(t, db, "evt-s"+trID, trID, link.LinkID, "kp1")
		_ = i
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.Reverified != 1 {
		t.Fatalf("expected 1 reverify, got %d (actions=%+v)", actions.Reverified, actions)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusVerified {
		t.Errorf("status = %q, want verified", updated.Status)
	}
}

func TestProcessLinkSignals_UserCorrectionCountsWithWeight(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// 1 explicit failure + 1 user_correction (weight=2) = failure_n=3, meets weaken_failure_min=3.
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "q", "t1", "gap", "short", []string{})
	seedActivationFailureEvent(t, db, "evt-f1", "tr1", link.LinkID, "kp1")

	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr2", "ans2", "q", "t1", "confident", "short", []string{"kp1"})
	seedUserCorrectionEvent(t, db, "evt-c1", "tr2", []string{link.LinkID})

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.Weakened != 1 {
		t.Fatalf("expected weaken via correction weight, got %d", actions.Weakened)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.FailCount != 3 {
		t.Errorf("fail_count = %d, want 3 (1 failure + correction_weight=2)", updated.FailCount)
	}
}

func TestProcessLinkSignals_SkipsPromotionWhenKPNotCurrent(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp1'`)

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	for _, trID := range []string{"tr1", "tr2", "tr3"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "confident", "short", []string{"kp1"})
		seedActivationSuccessEvent(t, db, "evt-s"+trID, trID, link.LinkID, "kp1")
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.PendingPromotions != 0 || actions.Promoted != 0 {
		t.Errorf("expected no promotion for non-current KP, got pending=%d promoted=%d", actions.PendingPromotions, actions.Promoted)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusCandidate {
		t.Errorf("status = %q, want candidate (unpromoted)", updated.Status)
	}
	if updated.AdoptCount != 0 {
		t.Errorf("adopt_count = %d, want 0 (suppressed for non-current KP)", updated.AdoptCount)
	}
}

func TestProcessLinkSignals_WeakenStillAppliesWhenKPNotCurrent(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp1'`)

	for _, trID := range []string{"tr1", "tr2", "tr3"} {
		seedAnswer(t, db, "ans"+trID)
		seedTrace(t, db, trID, "ans"+trID, "q", "t1", "gap", "short", []string{})
		seedActivationFailureEvent(t, db, "evt-f"+trID, trID, link.LinkID, "kp1")
	}

	var actions LearningActionsSummary
	if err := svc.processLinkSignals(&actions); err != nil {
		t.Fatalf("processLinkSignals: %v", err)
	}
	if actions.Weakened != 1 {
		t.Fatalf("expected weaken to still apply for a non-current KP, got %d", actions.Weakened)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusWeakened {
		t.Errorf("status = %q, want weakened", updated.Status)
	}
	if updated.FailCount != 3 {
		t.Errorf("fail_count = %d, want 3 (failure accumulation is not suppressed for non-current KP)", updated.FailCount)
	}
}

func TestEvictIdle_Candidate(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	backdateLinkCreatedAt(t, db, link.LinkID, 31) // > candidate_idle_days=30

	var actions LearningActionsSummary
	if err := svc.evictIdle(&actions); err != nil {
		t.Fatalf("evictIdle: %v", err)
	}
	if actions.Deprecated != 1 {
		t.Fatalf("expected 1 deprecated, got %d", actions.Deprecated)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusDeprecated {
		t.Errorf("status = %q, want deprecated", updated.Status)
	}
}

func TestEvictIdle_CandidateWithRecentSignal_NotEvicted(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	backdateLinkCreatedAt(t, db, link.LinkID, 31)

	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "q", "t1", "confident", "short", []string{"kp1"})
	seedActivationFailureEvent(t, db, "evt-f1", "tr1", link.LinkID, "kp1")

	var actions LearningActionsSummary
	if err := svc.evictIdle(&actions); err != nil {
		t.Fatalf("evictIdle: %v", err)
	}
	if actions.Deprecated != 0 {
		t.Errorf("expected no eviction when a recent event exists, got %d deprecated", actions.Deprecated)
	}
}

func TestEvictIdle_Weakened(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	backdateLinkStatusChangedAt(t, db, link.LinkID, 61) // > deprecate_idle_days=60

	var actions LearningActionsSummary
	if err := svc.evictIdle(&actions); err != nil {
		t.Fatalf("evictIdle: %v", err)
	}
	if actions.Deprecated != 1 {
		t.Fatalf("expected 1 deprecated, got %d", actions.Deprecated)
	}
	updated, _ := activationSvc.GetLink(link.LinkID)
	if updated.Status != activation.StatusDeprecated {
		t.Errorf("status = %q, want deprecated", updated.Status)
	}
}

func TestEvictIdle_WeakenedWithRecentSuccess_NotEvicted(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	backdateLinkStatusChangedAt(t, db, link.LinkID, 61)

	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "q", "t1", "confident", "short", []string{"kp1"})
	seedActivationSuccessEvent(t, db, "evt-s1", "tr1", link.LinkID, "kp1")

	var actions LearningActionsSummary
	if err := svc.evictIdle(&actions); err != nil {
		t.Fatalf("evictIdle: %v", err)
	}
	if actions.Deprecated != 0 {
		t.Errorf("expected no eviction when a recent success exists, got %d deprecated", actions.Deprecated)
	}
}

func TestRun_ReportIncludesLearningActionsAndFastPathRate(t *testing.T) {
	svc, store, _, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.LearningActions.CreatedCandidates != 1 {
		t.Errorf("run result created_candidates = %d, want 1", result.LearningActions.CreatedCandidates)
	}

	content, _ := store.GetReport(result.ReportID)
	var report Report
	json.Unmarshal([]byte(*content), &report)
	if report.LearningActions.CreatedCandidates != 1 {
		t.Errorf("report created_candidates = %d, want 1", report.LearningActions.CreatedCandidates)
	}
	// No fast-path traces seeded, so fast_path_rate should be 0, not absent.
	if report.Summary.FastPathRate != 0 {
		t.Errorf("fast_path_rate = %f, want 0", report.Summary.FastPathRate)
	}
}

func TestRun_FastPathRateComputed(t *testing.T) {
	svc, store, _, db := setupStudyWithActivation(t)
	seedAnswer(t, db, "ans1")
	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr1", "ans1", "q1", "t1", "confident", "short", []string{})
	seedTrace(t, db, "tr2", "ans2", "q2", "t2", "confident", "short", []string{})
	db.Exec(`UPDATE traces SET path_type = 'fast' WHERE trace_id = 'tr1'`)

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	content, _ := store.GetReport(result.ReportID)
	var report Report
	json.Unmarshal([]byte(*content), &report)
	if report.Summary.FastPathRate != 0.5 {
		t.Errorf("fast_path_rate = %f, want 0.5", report.Summary.FastPathRate)
	}
}

func TestHandler_ListResults_And_GetResult(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)
	handler := NewHandler(svc)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	link, err := activationSvc.CreateLink("t1", activation.LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	lr := &activation.LearningResult{
		Action: activation.ActionCreateCandidate, ObjectType: activation.ObjectTypeActivationLink,
		ObjectID: link.LinkID, Reason: "test reason", Status: activation.ResultApplied,
	}
	if err := activationSvc.Store().InsertLearningResult(lr); err != nil {
		t.Fatalf("insert learning result: %v", err)
	}

	results, err := svc.store.ListLearningResults("", "activation_link", "", "", 50)
	if err != nil {
		t.Fatalf("ListLearningResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].QuestionTerms != "t1" || results[0].PointSummary != "content" {
		t.Errorf("expected joined question_terms/point_summary, got %+v", results[0])
	}

	detail, err := svc.store.GetLearningResult(results[0].ResultID)
	if err != nil || detail == nil {
		t.Fatalf("GetLearningResult: %v", err)
	}
	if detail.Reason != "test reason" {
		t.Errorf("reason = %q, want %q", detail.Reason, "test reason")
	}

	_ = handler
}
