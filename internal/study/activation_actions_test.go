package study

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
)

func seedActivationGapEvent(t *testing.T, db *sql.DB, eventID, traceID, questionTerms string, directPointIDs []string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"question_terms": questionTerms, "direct_point_ids": directPointIDs,
	})
	seedLearningEvent(t, db, eventID, traceID, "activation_gap", string(payload))
}

func setupStudyWithActivation(t *testing.T) (*Service, *Store, *activation.Service, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfig(), activationSvc, nil, 0, 0, 0)
	return svc, store, activationSvc, db
}

func TestCreateCandidates_SourceA_Cooccurrence(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})
	seedActivationGapEvent(t, db, "gap-evt-1", "tr1", "并发 问题", []string{"kp1"})
	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr2", "ans2", "并发怎么处理", "并发 问题", "confident", "short", []string{"kp1"})
	seedActivationGapEvent(t, db, "gap-evt-2", "tr2", "并发 问题", []string{"kp1"})

	// Step 1 scan first so link_candidates has the row.
	if _, err := svc.store.ScanCandidates(0.55, 0.03, 200); err != nil {
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

	var eventIDs []string
	if err := json.Unmarshal([]byte(results[0].EventIDs), &eventIDs); err != nil {
		t.Fatalf("unmarshal event_ids: %v", err)
	}
	if len(eventIDs) == 0 {
		t.Fatal("expected create_candidate event_ids to list supporting activation_gap events, got empty")
	}
	for _, eid := range eventIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM learning_events WHERE event_id = ?`, eid).Scan(&n); err != nil || n != 1 {
			t.Fatalf("event_id %q must exist in learning_events (n=%d err=%v)", eid, n, err)
		}
		var candN int
		if err := db.QueryRow(`SELECT COUNT(*) FROM link_candidates WHERE candidate_id = ?`, eid).Scan(&candN); err != nil {
			t.Fatalf("lookup link_candidates: %v", err)
		}
		if candN != 0 {
			t.Fatalf("event_ids must not contain link_candidates.candidate_id %q", eid)
		}
	}
}

func TestCreateCandidates_Idempotent(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})
	svc.store.ScanCandidates(0.55, 0.03, 200)

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
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedCooccurrence(t, db, "并发 问题", "kp1", 10, 8)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "并发问题是什么", "并发 问题", "confident", "short", []string{"kp1"})
	svc.store.ScanCandidates(0.55, 0.03, 200)

	var actions LearningActionsSummary
	svc.createCandidates(&actions)
	link, _ := activationSvc.Store().GetByQuestionAndPoint("并发 问题", "kp1")
	if err := activationSvc.Store().UpdateStatus(link.LinkID, activation.StatusDeprecated); err != nil {
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

// TestCreateCandidates_SourceA_AggregatesAcrossLabels covers the 2026-07-18
// point-level aggregation fix: the same KP hit confidently under several
// subject labels (each below candidate_confident_min on its own) must still
// qualify on the aggregated total and produce ONE link for the point.
// Observed conditions are per-trace quadruples (not label-term intersection).
func TestCreateCandidates_SourceA_AggregatesAcrossLabels(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	// 三个标签各自 confident < 5，聚合后 3+2+2=7 ≥ 5，ratio 7/7=1.0。
	seedCooccurrence(t, db, "数据库 句柄 限制", "kp1", 3, 3)
	seedCooccurrence(t, db, "数据库 句柄 管理", "kp1", 2, 2)
	seedCooccurrence(t, db, "数据库 句柄 上限", "kp1", 2, 2)
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "句柄数超限怎么办", "数据库 句柄 限制", "confident", "short", []string{"kp1"})
	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr2", "ans2", "句柄怎么管理", "数据库 句柄 管理", "confident", "short", []string{"kp1"})
	seedAnswer(t, db, "ans3")
	seedTrace(t, db, "tr3", "ans3", "句柄上限多少", "数据库 句柄 上限", "confident", "short", []string{"kp1"})

	if _, err := svc.store.ScanCandidates(0.55, 0.03, 200); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// 快照应是一行、代表标签为 confident 最高的"数据库 句柄 限制"、计数为聚合值。
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM link_candidates WHERE point_id = 'kp1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 aggregated candidate row, got %d", n)
	}
	var qterms string
	var cc, hc int
	db.QueryRow(`SELECT question_terms, confident_count, hit_count FROM link_candidates WHERE point_id = 'kp1'`).
		Scan(&qterms, &cc, &hc)
	if qterms != "数据库 句柄 限制" || cc != 7 || hc != 7 {
		t.Errorf("unexpected aggregated candidate: terms=%s cc=%d hc=%d", qterms, cc, hc)
	}

	var actions LearningActionsSummary
	if err := svc.createCandidates(&actions); err != nil {
		t.Fatalf("createCandidates: %v", err)
	}
	if actions.CreatedCandidates != 1 {
		t.Fatalf("expected 1 created candidate, got %d", actions.CreatedCandidates)
	}

	links, err := activationSvc.Store().ListLinks(activation.ListLinksFilter{PointID: "kp1"})
	if err != nil || len(links) != 1 {
		t.Fatalf("expected exactly 1 link for point, got %d (err=%v)", len(links), err)
	}
	if len(links[0].ObservedConditions) != 3 {
		t.Fatalf("observed_conditions = %d, want 3 quadruples (one per confident trace)", len(links[0].ObservedConditions))
	}

	// 再跑一轮：同一 point 不得因代表标签或来源不同再建第二条链接。
	var actions2 LearningActionsSummary
	if err := svc.createCandidates(&actions2); err != nil {
		t.Fatalf("createCandidates 2nd: %v", err)
	}
	if actions2.CreatedCandidates != 0 {
		t.Errorf("expected point-level dedup to block recreation, got %d", actions2.CreatedCandidates)
	}
}

func TestCreateCandidates_SourceB_ActivationGap(t *testing.T) {
	svc, _, activationSvc, db := setupStudyWithActivation(t)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
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
	seedEntry(t, db, "con1", "dom1", "C")
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

func TestRun_ReportIncludesLearningActionsAndFastPathRate(t *testing.T) {
	svc, store, _, db := setupStudyWithActivation(t)
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
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
	seedEntry(t, db, "con1", "dom1", "C")
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
