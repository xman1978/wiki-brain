package trace

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func decodePayload(t *testing.T, payload string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("decode payload %q: %v", payload, err)
	}
	return m
}

func TestProcessTrace_FastPath_HitCited_ProducesActivationSuccess(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fast-1")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-fast-1",
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

	traces, err := store.ListTraces("", "a-fast-1", "", 20, 0)
	if err != nil || len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d (err=%v)", len(traces), err)
	}
	full, _ := store.GetTrace(traces[0].TraceID)
	if full.PathType != retrieval.PathTypeFast {
		t.Errorf("path_type = %q, want fast", full.PathType)
	}
	if len(full.ActivationLinkIDs) != 1 || full.ActivationLinkIDs[0] != "link1" {
		t.Errorf("activation_link_ids = %v, want [link1]", full.ActivationLinkIDs)
	}

	events, err := store.ListLearningEvents("activation_success", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_success event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["link_id"] != "link1" || payload["point_id"] != "p1" {
		t.Errorf("unexpected payload: %+v", payload)
	}
	if payload["match_score"].(float64) != 0.9 {
		t.Errorf("match_score = %v, want 0.9", payload["match_score"])
	}
	factIDs, _ := payload["cited_fact_ids"].([]interface{})
	if len(factIDs) != 1 || factIDs[0] != "f1" {
		t.Errorf("cited_fact_ids = %v, want [f1]", payload["cited_fact_ids"])
	}
	if events[0].TraceID != traces[0].TraceID {
		t.Errorf("event trace_id = %q, want %q", events[0].TraceID, traces[0].TraceID)
	}

	gapEvents, _ := store.ListLearningEvents("activation_gap", 0, 20)
	if len(gapEvents) != 0 {
		t.Errorf("expected no activation_gap when hits present, got %d", len(gapEvents))
	}
}

func TestProcessTrace_FastPath_HitCitedAsSupporting_ProducesActivationSuccessSupporting(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fast-supporting")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	r := &answer.AnswerResult{
		AnswerID:  "a-fast-supporting",
		Question:  "住宿标准是什么",
		Citations: []string{"f1", "f2"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{
				{LinkID: "link1", PointID: "p1", MatchScore: 0.9},
				{LinkID: "link2", PointID: "p2", MatchScore: 0.8},
			},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
			Supporting:     []retrieval.Evidence{{FactID: "f2", PointID: "p2"}},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_success", 0, 20)
	if err != nil || len(events) != 2 {
		t.Fatalf("expected 2 activation_success events, got %d (err=%v)", len(events), err)
	}
	byLink := make(map[string]map[string]interface{})
	for _, ev := range events {
		p := decodePayload(t, ev.Payload)
		byLink[p["link_id"].(string)] = p
	}
	direct, ok := byLink["link1"]
	if !ok || direct["role"] != "direct" {
		t.Errorf("link1 payload = %+v, want role=direct", direct)
	}
	supporting, ok := byLink["link2"]
	if !ok || supporting["role"] != "supporting" {
		t.Errorf("link2 payload = %+v, want role=supporting", supporting)
	}
	factIDs, _ := supporting["cited_fact_ids"].([]interface{})
	if len(factIDs) != 1 || factIDs[0] != "f2" {
		t.Errorf("supporting cited_fact_ids = %v, want [f2]", supporting["cited_fact_ids"])
	}

	failureEvents, _ := store.ListLearningEvents("activation_failure", 0, 20)
	if len(failureEvents) != 0 {
		t.Errorf("expected no activation_failure, got %d", len(failureEvents))
	}
}

func TestProcessTrace_FastPath_HitNotCited_ProducesActivationFailure_NotCited(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fast-2")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	r := &answer.AnswerResult{
		AnswerID:  "a-fast-2",
		Question:  "住宿标准是什么",
		Citations: []string{"f2"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.8}},
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
				{FactID: "f2", PointID: "p2"},
			},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_failure", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_failure event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["reason"] != "not_cited" {
		t.Errorf("reason = %v, want not_cited", payload["reason"])
	}
	if payload["link_id"] != "link1" {
		t.Errorf("link_id = %v, want link1", payload["link_id"])
	}
}

func TestProcessTrace_FastPath_AnswerError_ReasonAnswerError(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fast-3")

	r := &answer.AnswerResult{
		AnswerID:  "a-fast-3",
		Question:  "住宿标准是什么",
		Citations: []string{},
		HasAnswer: false,
		Path:      "error",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.8}},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_failure", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_failure event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["reason"] != "answer_error" {
		t.Errorf("reason = %v, want answer_error", payload["reason"])
	}
}

func TestProcessTrace_FastPath_GapQuality_ReasonAnswerGap(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fast-4")

	r := &answer.AnswerResult{
		AnswerID:  "a-fast-4",
		Question:  "住宿标准是什么",
		Citations: []string{},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.8}},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_failure", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_failure event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["reason"] != "answer_gap" {
		t.Errorf("reason = %v, want answer_gap", payload["reason"])
	}
}

func TestProcessTrace_FullPath_ConfidentNoHits_ProducesActivationGap(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-full-1")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-full-1",
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

	events, err := store.ListLearningEvents("activation_gap", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_gap event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	pids, _ := payload["direct_point_ids"].([]interface{})
	if len(pids) != 1 || pids[0] != "p1" {
		t.Errorf("direct_point_ids = %v, want [p1]", payload["direct_point_ids"])
	}
	// insertTestKP leaves the KU's entry_id NULL, so the point is 100%
	// unanchored — above the 0.7 threshold configured in setupService.
	if payload["gap_level"] != "entry_gap" {
		t.Errorf("gap_level = %v, want entry_gap", payload["gap_level"])
	}
	if ratio, ok := payload["null_entry_ratio"].(float64); !ok || ratio != 1.0 {
		t.Errorf("null_entry_ratio = %v, want 1.0", payload["null_entry_ratio"])
	}

	successEvents, _ := store.ListLearningEvents("activation_success", 0, 20)
	failureEvents, _ := store.ListLearningEvents("activation_failure", 0, 20)
	if len(successEvents) != 0 || len(failureEvents) != 0 {
		t.Errorf("expected no success/failure events for full path with no hits, got success=%d failure=%d",
			len(successEvents), len(failureEvents))
	}
}

func TestProcessTrace_FullPath_ConceptAnchored_ProducesLinkGap(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-full-concept")
	insertTestEntry(t, db, "c-anchored", sql.NullString{})
	insertTestKPWithEntry(t, db, "p-anchored", "u-anchored", "c-anchored")

	r := &answer.AnswerResult{
		AnswerID:  "a-full-concept",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFull,
			ActivationHits: []retrieval.ActivationHit{},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p-anchored"}},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_gap", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_gap event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["gap_level"] != "link_gap" {
		t.Errorf("gap_level = %v, want link_gap", payload["gap_level"])
	}
	if ratio, ok := payload["null_entry_ratio"].(float64); !ok || ratio != 0.0 {
		t.Errorf("null_entry_ratio = %v, want 0.0", payload["null_entry_ratio"])
	}
}

func TestProcessTrace_FullPath_MergedConceptAnchor_CountsAsNull(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-full-merged")
	insertTestEntry(t, db, "c-target", sql.NullString{})
	insertTestEntry(t, db, "c-merged", sql.NullString{String: "c-target", Valid: true})
	insertTestKPWithEntry(t, db, "p-merged", "u-merged", "c-merged")

	r := &answer.AnswerResult{
		AnswerID:  "a-full-merged",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFull,
			ActivationHits: []retrieval.ActivationHit{},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p-merged"}},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_gap", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 activation_gap event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	// The KP's concept was merged into another one, so it no longer counts
	// as a current anchor — same treatment as entry_id being NULL.
	if payload["gap_level"] != "entry_gap" {
		t.Errorf("gap_level = %v, want entry_gap (merged concept doesn't count as anchored)", payload["gap_level"])
	}
	if ratio, ok := payload["null_entry_ratio"].(float64); !ok || ratio != 1.0 {
		t.Errorf("null_entry_ratio = %v, want 1.0", payload["null_entry_ratio"])
	}
}

func TestProcessTrace_FullPath_PartialNoHits_NoActivationGap(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-full-2")
	insertTestKP(t, db, "p2")

	r := &answer.AnswerResult{
		AnswerID:  "a-full-2",
		Question:  "住宿标准是什么",
		Citations: []string{"f2"},
		HasAnswer: true,
		Path:      "deep",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFull,
			Supporting: []retrieval.Evidence{
				{FactID: "f2", PointID: "p2"},
			},
		},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_gap", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("expected no activation_gap for partial quality, got %d (err=%v)", len(events), err)
	}
}

func TestProcessTrace_FullPath_GapQuality_NoActivationGap(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-full-3")

	r := &answer.AnswerResult{
		AnswerID:    "a-full-3",
		Question:    "未知问题",
		Citations:   []string{},
		HasAnswer:   false,
		Path:        "none",
		EvidenceSet: &retrieval.EvidenceSet{PathType: retrieval.PathTypeFull},
	}
	svc.ProcessTrace(r)

	events, err := store.ListLearningEvents("activation_gap", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("expected no activation_gap for gap quality, got %d (err=%v)", len(events), err)
	}
}

func TestProcessTrace_WikiPath_NoActivationEvents(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-wiki-1")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-wiki-1",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeWiki,
			ActivationHits: []retrieval.ActivationHit{{LinkID: "link1", PointID: "p1", MatchScore: 0.9}},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
		},
	}
	svc.ProcessTrace(r)

	for _, eventType := range []string{"activation_success", "activation_failure", "activation_gap"} {
		events, err := store.ListLearningEvents(eventType, 0, 20)
		if err != nil {
			t.Fatalf("list %s: %v", eventType, err)
		}
		if len(events) != 0 {
			t.Errorf("expected no %s events for wiki path, got %d", eventType, len(events))
		}
	}
}

func TestProcessTrace_MultipleHits_OneEventPerLinkSameTraceID(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-multi-1")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	r := &answer.AnswerResult{
		AnswerID:  "a-multi-1",
		Question:  "住宿标准是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			ActivationHits: []retrieval.ActivationHit{
				{LinkID: "link1", PointID: "p1", MatchScore: 0.9},
				{LinkID: "link2", PointID: "p2", MatchScore: 0.8},
			},
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
				{FactID: "f2", PointID: "p2"},
			},
		},
	}
	svc.ProcessTrace(r)

	traces, _ := store.ListTraces("", "a-multi-1", "", 20, 0)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	traceID := traces[0].TraceID

	successEvents, _ := store.ListLearningEvents("activation_success", 0, 20)
	failureEvents, _ := store.ListLearningEvents("activation_failure", 0, 20)
	if len(successEvents) != 1 || len(failureEvents) != 1 {
		t.Fatalf("expected 1 success + 1 failure, got success=%d failure=%d", len(successEvents), len(failureEvents))
	}
	if successEvents[0].TraceID != traceID || failureEvents[0].TraceID != traceID {
		t.Errorf("events should share trace_id %q, got success=%q failure=%q",
			traceID, successEvents[0].TraceID, failureEvents[0].TraceID)
	}
}

func TestSubmitFeedback_FastPath_CarriesLinkIDs(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fb-fast")

	store.SaveTrace(&Trace{
		TraceID:           "t-fb-fast",
		AnswerID:          "a-fb-fast",
		Question:          "q",
		QuestionHash:      "h1",
		QuestionTerms:     "t",
		RetrievalQuality:  QualityConfident,
		Path:              "short",
		PathType:          retrieval.PathTypeFast,
		ActivationLinkIDs: []string{"link1", "link2"},
		DirectPointIDs:    []string{},
	})

	tr, _ := store.GetTrace("t-fb-fast")
	if err := svc.SubmitFeedback(tr, FeedbackRequest{Type: "negative", Content: "wrong"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	events, _ := store.ListLearningEvents("user_correction", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 user_correction event, got %d", len(events))
	}
	payload := decodePayload(t, events[0].Payload)
	linkIDs, ok := payload["link_ids"].([]interface{})
	if !ok || len(linkIDs) != 2 || linkIDs[0] != "link1" || linkIDs[1] != "link2" {
		t.Errorf("link_ids = %v, want [link1 link2]", payload["link_ids"])
	}
}

func TestSubmitFeedback_FullPath_NoLinkIDsField(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fb-full")

	store.SaveTrace(&Trace{
		TraceID:          "t-fb-full",
		AnswerID:         "a-fb-full",
		Question:         "q",
		QuestionHash:     "h2",
		QuestionTerms:    "t",
		RetrievalQuality: QualityConfident,
		Path:             "short",
		PathType:         retrieval.PathTypeFull,
		DirectPointIDs:   []string{},
	})

	tr, _ := store.GetTrace("t-fb-full")
	if err := svc.SubmitFeedback(tr, FeedbackRequest{Type: "negative", Content: "wrong"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	events, _ := store.ListLearningEvents("user_correction", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 user_correction event, got %d", len(events))
	}
	payload := decodePayload(t, events[0].Payload)
	if _, ok := payload["link_ids"]; ok {
		t.Errorf("expected no link_ids field for full-path trace, got %v", payload["link_ids"])
	}
}

func TestStore_ListTraces_FilterByPathType(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	insertTestAnswer(t, db, "a-pt-1")
	insertTestAnswer(t, db, "a-pt-2")

	store.SaveTrace(&Trace{
		TraceID: "t-pt-1", AnswerID: "a-pt-1", Question: "q", QuestionHash: "h1", QuestionTerms: "t",
		RetrievalQuality: QualityConfident, Path: "short", PathType: retrieval.PathTypeFast, DirectPointIDs: []string{},
	})
	store.SaveTrace(&Trace{
		TraceID: "t-pt-2", AnswerID: "a-pt-2", Question: "q", QuestionHash: "h2", QuestionTerms: "t",
		RetrievalQuality: QualityConfident, Path: "short", PathType: retrieval.PathTypeFull, DirectPointIDs: []string{},
	})

	fastTraces, err := store.ListTraces("", "", retrieval.PathTypeFast, 20, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(fastTraces) != 1 || fastTraces[0].TraceID != "t-pt-1" {
		t.Errorf("expected 1 fast trace (t-pt-1), got %+v", fastTraces)
	}
}
