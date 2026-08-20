package trace

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// TestGenerateActivationEvents_RecordOutcome_DirectAndSupporting_UseHitQuadruple
// asserts that generateActivationEvents calls RecordOutcome once per hit,
// using the hit's own stored quadruple (not the query's — see
// activation.md「owning condition 的可判定性」/ trace.md 步骤 3), with
// success=true for both role=direct and role=supporting outcomes (2026-08-13:
// role no longer carries statistical weight, docs/impl/v1/trace.md 步骤 3
// payload 注释).
func TestGenerateActivationEvents_RecordOutcome_DirectAndSupporting_UseHitQuadruple(t *testing.T) {
	svc, _, db := setupService(t)
	insertTestAnswer(t, db, "a-ro-1")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-ro-1",
		Question:  "住宿标准是什么",
		Citations: []string{"f1", "f2"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{
				{LinkID: "link1", PointID: "p1", MatchScore: 0.9,
					Subject: "住宿标准", Intent: "查询", Audience: "全员", Constraint: "国内"},
				{LinkID: "link2", PointID: "p2", MatchScore: 0.8,
					Subject: "差旅报销", Intent: "查询", Audience: "全员", Constraint: ""},
			},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
			Supporting:     []retrieval.Evidence{{FactID: "f2", PointID: "p2"}},
			// Query quadruple deliberately differs from the hits' own stored
			// quadruples above, so a test that used the query's fields
			// instead of the hit's would fail this assertion.
			Subject:    "query-subject",
			Intent:     "query-intent",
			Audience:   "query-audience",
			Constraint: "query-constraint",
		},
	}
	svc.ProcessTrace(r)

	if len(fake.recordOutcomeCalls) != 2 {
		t.Fatalf("expected 2 RecordOutcome calls, got %d: %+v", len(fake.recordOutcomeCalls), fake.recordOutcomeCalls)
	}

	byLink := make(map[string]recordOutcomeCall)
	for _, c := range fake.recordOutcomeCalls {
		byLink[c.linkID] = c
	}

	direct, ok := byLink["link1"]
	if !ok {
		t.Fatalf("expected RecordOutcome call for link1, got %+v", fake.recordOutcomeCalls)
	}
	if !direct.success {
		t.Errorf("role=direct: expected success=true, got %v", direct.success)
	}
	if direct.subject != "住宿标准" || direct.intent != "查询" || direct.audience != "全员" || direct.constraint != "国内" {
		t.Errorf("role=direct: quadruple = %+v, want the hit's own stored quadruple, not the query's", direct)
	}

	supporting, ok := byLink["link2"]
	if !ok {
		t.Fatalf("expected RecordOutcome call for link2, got %+v", fake.recordOutcomeCalls)
	}
	if !supporting.success {
		t.Errorf("role=supporting: expected success=true, got %v", supporting.success)
	}
	if supporting.subject != "差旅报销" || supporting.audience != "全员" {
		t.Errorf("role=supporting: quadruple = %+v, want the hit's own stored quadruple, not the query's", supporting)
	}
}

// TestGenerateActivationEvents_RecordOutcome_NotCited_Failure asserts a hit
// that's neither direct nor supporting-cited calls RecordOutcome(success=false).
func TestGenerateActivationEvents_RecordOutcome_NotCited_Failure(t *testing.T) {
	svc, _, db := setupService(t)
	insertTestAnswer(t, db, "a-ro-2")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-ro-2",
		Question:  "住宿标准是什么",
		Citations: []string{"f2"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{
				{LinkID: "link1", PointID: "p1", MatchScore: 0.8,
					Subject: "住宿标准", Intent: "查询", Audience: "全员", Constraint: ""},
			},
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
				{FactID: "f2", PointID: "p2"},
			},
		},
	}
	svc.ProcessTrace(r)

	if len(fake.recordOutcomeCalls) != 1 {
		t.Fatalf("expected 1 RecordOutcome call, got %d: %+v", len(fake.recordOutcomeCalls), fake.recordOutcomeCalls)
	}
	call := fake.recordOutcomeCalls[0]
	if call.linkID != "link1" || call.success {
		t.Errorf("expected RecordOutcome(link1, success=false), got %+v", call)
	}
	if call.subject != "住宿标准" {
		t.Errorf("expected hit's own subject, got %q", call.subject)
	}
}

// TestGenerateActivationEvents_Gap_NoRecordOutcome asserts activation_gap
// (no hits, full-path confident) does not call RecordOutcome — that event
// stays on Study's existing periodic-scan candidate-creation path,
// unaffected by this confidence rewrite (docs/impl/v1/trace.md 完成标准).
func TestGenerateActivationEvents_Gap_NoRecordOutcome(t *testing.T) {
	svc, _, db := setupService(t)
	insertTestAnswer(t, db, "a-ro-3")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-ro-3",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFull,
			ActivationHits: []retrieval.ActivationHit{},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(r)

	if len(fake.recordOutcomeCalls) != 0 {
		t.Errorf("expected no RecordOutcome calls for activation_gap, got %d", len(fake.recordOutcomeCalls))
	}
	// The full-path confident case is exactly the enrichment gate, so
	// EnrichFromConfidentFullPath should fire instead — the two are mutually
	// exclusive per point on a single question (see the dedicated exclusivity
	// test below).
	if fake.enrichCalls != 1 {
		t.Errorf("expected EnrichFromConfidentFullPath called once, got %d", fake.enrichCalls)
	}
}

// TestFastPathVsSlowPath_RecordOutcomeAndEnrichment_MutuallyExclusive verifies
// the documented invariant (docs/impl/v1/trace.md 完成标准): a single
// question's processing triggers RecordOutcome (fast path, hits present) XOR
// EnrichFromConfidentFullPath (slow path, no hits, confident), never both,
// never neither (when there's a citable direct point).
func TestFastPathVsSlowPath_RecordOutcomeAndEnrichment_MutuallyExclusive(t *testing.T) {
	svc, _, db := setupService(t)
	insertTestAnswer(t, db, "a-excl-fast")
	insertTestAnswer(t, db, "a-excl-slow")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	// Fast path: hit present, cited as direct -> RecordOutcome fires, no
	// enrichment (enrichObservedConditions only runs for path_type=full).
	fastResult := &answer.AnswerResult{
		AnswerID:  "a-excl-fast",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.9}},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(fastResult)

	if len(fake.recordOutcomeCalls) != 1 {
		t.Fatalf("fast path: expected 1 RecordOutcome call, got %d", len(fake.recordOutcomeCalls))
	}
	if fake.enrichCalls != 0 {
		t.Fatalf("fast path: expected 0 enrichment calls, got %d", fake.enrichCalls)
	}

	// Reset counters, then run the slow path: no hits, confident, full path
	// -> enrichment fires, no RecordOutcome (activation_hits is empty so the
	// generateActivationEvents hit loop never runs).
	fake.recordOutcomeCalls = nil
	fake.enrichCalls = 0

	slowResult := &answer.AnswerResult{
		AnswerID:  "a-excl-slow",
		Question:  "住宿标准是什么2",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "deep",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFull,
			ActivationHits: []retrieval.ActivationHit{},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(slowResult)

	if len(fake.recordOutcomeCalls) != 0 {
		t.Fatalf("slow path: expected 0 RecordOutcome calls, got %d", len(fake.recordOutcomeCalls))
	}
	if fake.enrichCalls != 1 {
		t.Fatalf("slow path: expected 1 enrichment call, got %d", fake.enrichCalls)
	}
}

// TestSubmitFeedback_CorrectionWeight_CallsRecordOutcomeNTimesPerLink asserts
// negative/correction feedback on a trace with linked ActivationLinks calls
// RecordOutcome(success=false) exactly correction_weight times per link
// (docs/impl/v1/trace.md 步骤 4 / 完成标准).
func TestSubmitFeedback_CorrectionWeight_CallsRecordOutcomeNTimesPerLink(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-cw-1")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)
	svc.SetCorrectionWeight(2)

	store.SaveTrace(&Trace{
		TraceID:           "t-cw-1",
		AnswerID:          "a-cw-1",
		Question:          "q",
		QuestionHash:      "h-cw-1",
		QuestionTerms:     "t",
		RetrievalQuality:  QualityConfident,
		Path:              "short",
		PathType:          retrieval.PathTypeFast,
		ActivationLinkIDs: []string{"link1", "link2"},
		Subject:           "s", Intent: "i", Audience: "a", ConstraintText: "c",
		DirectPointIDs: []string{},
	})

	tr, _ := store.GetTrace("t-cw-1")
	if err := svc.SubmitFeedback(tr, FeedbackRequest{Type: "negative", Content: "wrong"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	if len(fake.recordOutcomeCalls) != 4 {
		t.Fatalf("expected 4 RecordOutcome calls (2 links x weight 2), got %d: %+v",
			len(fake.recordOutcomeCalls), fake.recordOutcomeCalls)
	}

	countByLink := map[string]int{}
	for _, c := range fake.recordOutcomeCalls {
		if c.success {
			t.Errorf("expected success=false for correction outcome, got %+v", c)
		}
		if c.subject != "s" || c.intent != "i" || c.audience != "a" || c.constraint != "c" {
			t.Errorf("expected trace's own query quadruple, got %+v", c)
		}
		countByLink[c.linkID]++
	}
	if countByLink["link1"] != 2 || countByLink["link2"] != 2 {
		t.Errorf("expected 2 calls per link, got %+v", countByLink)
	}
}

// TestSubmitFeedback_NoLinkIDs_NoRecordOutcome ensures the correction loop is
// skipped entirely when the trace carries no activation_link_ids (full-path
// traces).
func TestSubmitFeedback_NoLinkIDs_NoRecordOutcome(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-cw-2")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)
	svc.SetCorrectionWeight(2)

	store.SaveTrace(&Trace{
		TraceID:          "t-cw-2",
		AnswerID:         "a-cw-2",
		Question:         "q",
		QuestionHash:     "h-cw-2",
		QuestionTerms:    "t",
		RetrievalQuality: QualityConfident,
		Path:             "short",
		PathType:         retrieval.PathTypeFull,
		DirectPointIDs:   []string{},
	})

	tr, _ := store.GetTrace("t-cw-2")
	if err := svc.SubmitFeedback(tr, FeedbackRequest{Type: "negative", Content: "wrong"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	if len(fake.recordOutcomeCalls) != 0 {
		t.Errorf("expected no RecordOutcome calls when trace has no linked ActivationLinks, got %d", len(fake.recordOutcomeCalls))
	}
}

// TestWriteAuditOutcome_Agree_WritesSuccessEventAndCallsRecordAuditOutcome
// covers writeAuditOutcome's payload shape and RecordAuditOutcome wiring even
// though it has no production caller yet in this phase (Retrieval's
// background audit-trial orchestration lands in 阶段 4).
func TestWriteAuditOutcome_Agree_WritesSuccessEventAndCallsRecordAuditOutcome(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-audit-1")
	store.SaveTrace(&Trace{
		TraceID: "t-audit-1", AnswerID: "a-audit-1", Question: "q", QuestionHash: "h-audit-1",
		QuestionTerms: "t", RetrievalQuality: QualityConfident, Path: "short",
		PathType: retrieval.PathTypeFast, DirectPointIDs: []string{},
	})

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	err := svc.writeAuditOutcome("t-audit-1", "link1", "p1", "住宿标准", "查询", "全员", "国内",
		0.83, true, []string{"p1", "p2"})
	if err != nil {
		t.Fatalf("writeAuditOutcome: %v", err)
	}

	successEvents, _ := store.ListLearningEvents("activation_audit_success", 0, 20)
	if len(successEvents) != 1 {
		t.Fatalf("expected 1 activation_audit_success event, got %d", len(successEvents))
	}
	payload := decodePayload(t, successEvents[0].Payload)
	if payload["link_id"] != "link1" || payload["point_id"] != "p1" {
		t.Errorf("unexpected payload: %+v", payload)
	}
	if payload["agree"] != true {
		t.Errorf("agree = %v, want true", payload["agree"])
	}
	if payload["audited_trace_id"] != "t-audit-1" {
		t.Errorf("audited_trace_id = %v, want t-audit-1", payload["audited_trace_id"])
	}
	pids, _ := payload["slow_path_direct_point_ids"].([]interface{})
	if len(pids) != 2 {
		t.Errorf("slow_path_direct_point_ids = %v, want 2 entries", payload["slow_path_direct_point_ids"])
	}
	if _, hasReason := payload["reason"]; hasReason {
		t.Errorf("agree=true should not set reason, got %v", payload["reason"])
	}

	failureEvents, _ := store.ListLearningEvents("activation_audit_failure", 0, 20)
	if len(failureEvents) != 0 {
		t.Errorf("expected no activation_audit_failure events, got %d", len(failureEvents))
	}

	if len(fake.recordAuditOutcomeCalls) != 1 {
		t.Fatalf("expected 1 RecordAuditOutcome call, got %d", len(fake.recordAuditOutcomeCalls))
	}
	call := fake.recordAuditOutcomeCalls[0]
	if call.linkID != "link1" || !call.agree || call.subject != "住宿标准" {
		t.Errorf("unexpected RecordAuditOutcome call: %+v", call)
	}
}

// TestWriteAuditOutcome_Disagree_WritesFailureEventWithReason covers the
// agree=false branch: event type flips to activation_audit_failure, reason
// is point_not_in_slow_path (V1 scope, docs/impl/v1/trace.md 步骤 3b), and
// RecordAuditOutcome is still called (with agree=false).
func TestWriteAuditOutcome_Disagree_WritesFailureEventWithReason(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-audit-2")
	store.SaveTrace(&Trace{
		TraceID: "t-audit-2", AnswerID: "a-audit-2", Question: "q", QuestionHash: "h-audit-2",
		QuestionTerms: "t", RetrievalQuality: QualityConfident, Path: "short",
		PathType: retrieval.PathTypeFast, DirectPointIDs: []string{},
	})

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	err := svc.writeAuditOutcome("t-audit-2", "link1", "p1", "住宿标准", "查询", "全员", "",
		0.71, false, []string{"p2"})
	if err != nil {
		t.Fatalf("writeAuditOutcome: %v", err)
	}

	failureEvents, _ := store.ListLearningEvents("activation_audit_failure", 0, 20)
	if len(failureEvents) != 1 {
		t.Fatalf("expected 1 activation_audit_failure event, got %d", len(failureEvents))
	}
	payload := decodePayload(t, failureEvents[0].Payload)
	if payload["agree"] != false {
		t.Errorf("agree = %v, want false", payload["agree"])
	}
	if payload["reason"] != "point_not_in_slow_path" {
		t.Errorf("reason = %v, want point_not_in_slow_path", payload["reason"])
	}

	if len(fake.recordAuditOutcomeCalls) != 1 || fake.recordAuditOutcomeCalls[0].agree {
		t.Fatalf("expected 1 RecordAuditOutcome call with agree=false, got %+v", fake.recordAuditOutcomeCalls)
	}
}

// TestGenerateActivationEvents_RecordOutcomeFailure_DoesNotAbortTrace ensures
// a RecordOutcome error (e.g. the condition lookup fails) is logged and
// swallowed — cooccurrence updates and other trace_write steps must still
// complete (docs/impl/v1/trace.md 完成标准).
func TestGenerateActivationEvents_RecordOutcomeFailure_DoesNotAbortTrace(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-ro-err")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{recordOutcomeErr: errRecordOutcome}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-ro-err",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.9}},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(r)

	// The activation_success event itself must still have been written
	// despite RecordOutcome failing.
	events, _ := store.ListLearningEvents("activation_success", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 activation_success event even when RecordOutcome fails, got %d", len(events))
	}
	// Cooccurrence (a later trace_write step) must still have run.
	coocs, _ := store.ListCooccurrence("p1", 0, 50)
	if len(coocs) != 1 {
		t.Errorf("expected cooccurrence update to still complete, got %d rows", len(coocs))
	}
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

var errRecordOutcome = sentinelErr("record outcome failed")

// TestGenerateBundleActivationEvents_MemberCited_SuccessOnBothAxes covers
// the 2026-08-20「验证」接线: a Bundle hit whose member point ends up cited
// direct calls RecordBundleOutcome(success=true) using the hit's own stored
// quadruple, and RecordMemberOutcome(success=true) for each of the hit's
// MemberPointIDs — mirroring generateActivationEvents' Link behavior, just
// keyed on bundle/member instead of link.
func TestGenerateBundleActivationEvents_MemberCited_SuccessOnBothAxes(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-bo-1")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-bo-1",
		Question:  "绩效考核怎么算",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			BundleHits: []retrieval.BundleHit{
				{BundleID: "bundle1", MatchScore: 0.9,
					Subject: "绩效考核", Intent: "怎么算", Audience: "全员", Constraint: "",
					MemberPointIDs: []string{"p1", "p2"}},
			},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(r)

	successEvents, _ := store.ListLearningEvents("activation_success", 0, 20)
	if len(successEvents) != 1 {
		t.Fatalf("expected 1 activation_success event, got %d", len(successEvents))
	}
	payload := decodePayload(t, successEvents[0].Payload)
	if payload["bundle_id"] != "bundle1" {
		t.Errorf("unexpected payload: %+v", payload)
	}

	if len(fake.recordBundleOutcomeCalls) != 1 {
		t.Fatalf("expected 1 RecordBundleOutcome call, got %d: %+v", len(fake.recordBundleOutcomeCalls), fake.recordBundleOutcomeCalls)
	}
	bc := fake.recordBundleOutcomeCalls[0]
	if bc.bundleID != "bundle1" || !bc.success {
		t.Errorf("expected RecordBundleOutcome(bundle1, success=true), got %+v", bc)
	}
	if bc.subject != "绩效考核" || bc.intent != "怎么算" || bc.audience != "全员" {
		t.Errorf("expected the hit's own stored quadruple, got %+v", bc)
	}

	if len(fake.recordMemberOutcomeCalls) != 2 {
		t.Fatalf("expected 2 RecordMemberOutcome calls (one per member), got %d: %+v",
			len(fake.recordMemberOutcomeCalls), fake.recordMemberOutcomeCalls)
	}
	byPoint := make(map[string]bool)
	for _, c := range fake.recordMemberOutcomeCalls {
		if c.bundleID != "bundle1" {
			t.Errorf("expected bundle1, got %+v", c)
		}
		byPoint[c.pointID] = c.success
	}
	if !byPoint["p1"] {
		t.Errorf("expected p1 (cited direct) recorded as success=true, got %+v", fake.recordMemberOutcomeCalls)
	}
	if byPoint["p2"] {
		t.Errorf("expected p2 (not cited) recorded as success=false, got %+v", fake.recordMemberOutcomeCalls)
	}
}

// TestGenerateBundleActivationEvents_NoMemberCited_FailureOnBundleAxis
// covers the all-members-missed case: the Bundle's own trigger-axis outcome
// is failure, and every member is individually recorded as failure too.
func TestGenerateBundleActivationEvents_NoMemberCited_FailureOnBundleAxis(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-bo-2")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-bo-2",
		Question:  "绩效考核怎么算",
		Citations: []string{"f9"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			BundleHits: []retrieval.BundleHit{
				{BundleID: "bundle1", MatchScore: 0.9, MemberPointIDs: []string{"p1"}},
			},
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f9", PointID: "p9"},
				{FactID: "f1", PointID: "p1"},
			},
		},
	}
	svc.ProcessTrace(r)

	failureEvents, _ := store.ListLearningEvents("activation_failure", 0, 20)
	if len(failureEvents) != 1 {
		t.Fatalf("expected 1 activation_failure event, got %d", len(failureEvents))
	}

	if len(fake.recordBundleOutcomeCalls) != 1 || fake.recordBundleOutcomeCalls[0].success {
		t.Fatalf("expected RecordBundleOutcome(success=false), got %+v", fake.recordBundleOutcomeCalls)
	}
	if len(fake.recordMemberOutcomeCalls) != 1 || fake.recordMemberOutcomeCalls[0].success {
		t.Fatalf("expected RecordMemberOutcome(p1, success=false), got %+v", fake.recordMemberOutcomeCalls)
	}
}
