package trace

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func setupService(t *testing.T) (*Service, *Store, *sql.DB) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	svc := NewService(store, 0.7)
	return svc, store, db
}

func TestProcessTrace_Confident(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-001")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-001",
		Question:  "什么是互斥锁？",
		Content:   "互斥锁是...",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			Subject: "互斥锁",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
		},
	}

	svc.ProcessTrace(r)

	traces, _ := store.ListTraces(QualityConfident, "", "", 20, 0)
	if len(traces) != 1 {
		t.Fatalf("expected 1 confident trace, got %d", len(traces))
	}

	full, _ := store.GetTrace(traces[0].TraceID)
	if len(full.DirectPointIDs) != 1 || full.DirectPointIDs[0] != "p1" {
		t.Errorf("expected direct_point_ids=[p1], got %v", full.DirectPointIDs)
	}
	if full.Path != "short" {
		t.Errorf("expected path=short, got %q", full.Path)
	}

	coocs, _ := store.ListCooccurrence("p1", 0, 50)
	if len(coocs) != 1 {
		t.Fatalf("expected 1 cooccurrence, got %d", len(coocs))
	}
	if coocs[0].ConfidentCount != 1 {
		t.Errorf("expected confident_count=1, got %d", coocs[0].ConfidentCount)
	}
}

func TestProcessTrace_Gap_CreatesLearningEvent(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-002")

	r := &answer.AnswerResult{
		AnswerID:    "a-002",
		Question:    "未知问题",
		Content:     "无法回答",
		Citations:   []string{},
		Path:        "none",
		EvidenceSet: &retrieval.EvidenceSet{},
	}

	svc.ProcessTrace(r)

	events, _ := store.ListLearningEvents("knowledge_gap", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 knowledge_gap event, got %d", len(events))
	}
	// GapReason unset on the EvidenceSet and Path != "error" — docs/impl/v1/trace.md's
	// fallback value for this theoretical-edge-case combination.
	assertPayloadReason(t, events[0].Payload, "unspecified")
}

func TestProcessTrace_Gap_ReasonFromEvidenceSet(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-002b")

	r := &answer.AnswerResult{
		AnswerID:  "a-002b",
		Question:  "未知问题",
		Citations: []string{},
		Path:      "deep",
		EvidenceSet: &retrieval.EvidenceSet{
			GapReason: retrieval.GapReasonJudgeFiltered,
		},
	}

	svc.ProcessTrace(r)

	events, _ := store.ListLearningEvents("knowledge_gap", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 knowledge_gap event, got %d", len(events))
	}
	assertPayloadReason(t, events[0].Payload, "judge_filtered")
}

func TestProcessTrace_Gap_ReasonAnswerErrorTakesPriority(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-002c")

	r := &answer.AnswerResult{
		AnswerID:  "a-002c",
		Question:  "未知问题",
		Citations: []string{},
		Path:      "error",
		EvidenceSet: &retrieval.EvidenceSet{
			// Even with a GapReason set, a generation failure isn't really
			// about missing knowledge — answer_error must win.
			GapReason: retrieval.GapReasonNoCandidates,
		},
	}

	svc.ProcessTrace(r)

	events, _ := store.ListLearningEvents("knowledge_gap", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 knowledge_gap event, got %d", len(events))
	}
	assertPayloadReason(t, events[0].Payload, "answer_error")
}

func assertPayloadReason(t *testing.T, payload, want string) {
	t.Helper()
	var p map[string]string
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p["reason"] != want {
		t.Errorf("expected reason=%s, got %q (payload=%s)", want, p["reason"], payload)
	}
}

func TestProcessTrace_DuplicateQuestion_NoDuplicateCooccurrence(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-003")
	insertTestAnswer(t, db, "a-004")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-003",
		Question:  "什么是互斥锁？",
		Citations: []string{"f1"},
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			Subject: "互斥锁",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
		},
	}

	svc.ProcessTrace(r)

	// Same question again with different answer_id
	r2 := &answer.AnswerResult{
		AnswerID:  "a-004",
		Question:  "什么是互斥锁？",
		Citations: []string{"f1"},
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			Subject: "互斥锁",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
		},
	}
	svc.ProcessTrace(r2)

	// Should have 2 traces but cooccurrence count still 1
	traces, _ := store.ListTraces("", "", "", 20, 0)
	if len(traces) != 2 {
		t.Errorf("expected 2 traces, got %d", len(traces))
	}

	coocs, _ := store.ListCooccurrence("p1", 0, 50)
	if len(coocs) != 1 {
		t.Fatalf("expected 1 cooccurrence, got %d", len(coocs))
	}
	if coocs[0].HitCount != 1 {
		t.Errorf("dedup: expected hit_count=1, got %d", coocs[0].HitCount)
	}
}

// Citing only a Supporting-bucket fact now grades confident (see
// directCitedPointIDs) rather than partial, so cooccurrence increments
// confident_count for that point, not just hit_count.
func TestProcessTrace_SupportingOnlyCited_UpdatesConfidentCooccurrence(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-005")
	insertTestKP(t, db, "p1")
	insertTestKP(t, db, "p2")

	r := &answer.AnswerResult{
		AnswerID:  "a-005",
		Question:  "关于并发的问题",
		Citations: []string{"f2"},
		Path:      "deep",
		EvidenceSet: &retrieval.EvidenceSet{
			Subject: "并发",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
			Supporting: []retrieval.Evidence{
				{FactID: "f2", PointID: "p2"},
			},
		},
	}

	svc.ProcessTrace(r)

	coocs, _ := store.ListCooccurrence("p2", 0, 50)
	if len(coocs) != 1 {
		t.Fatalf("expected 1 cooccurrence for supporting, got %d", len(coocs))
	}
	if coocs[0].HitCount != 1 {
		t.Errorf("expected hit_count=1, got %d", coocs[0].HitCount)
	}
	if coocs[0].ConfidentCount != 1 {
		t.Errorf("expected confident_count=1, got %d", coocs[0].ConfidentCount)
	}
}

func TestSubmitFeedback_Negative_CreatesEvent(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-fb")

	store.SaveTrace(&Trace{
		TraceID:          "t-fb",
		AnswerID:         "a-fb",
		Question:         "q",
		QuestionHash:     "h",
		QuestionTerms:    "t",
		RetrievalQuality: QualityConfident,
		Path:             "short",
		DirectPointIDs:   []string{},
	})

	tr0, _ := store.GetTrace("t-fb")
	err := svc.SubmitFeedback(tr0, FeedbackRequest{Type: "negative", Content: "wrong"})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	events, _ := store.ListLearningEvents("user_correction", 0, 20)
	if len(events) != 1 {
		t.Fatalf("expected 1 user_correction event, got %d", len(events))
	}

	tr, _ := store.GetTrace("t-fb")
	if !tr.HasFeedback {
		t.Error("expected has_feedback=true")
	}
}

func TestSubmitFeedback_Positive_NoEvent(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-pos")

	store.SaveTrace(&Trace{
		TraceID:          "t-pos",
		AnswerID:         "a-pos",
		Question:         "q",
		QuestionHash:     "h",
		QuestionTerms:    "t",
		RetrievalQuality: QualityConfident,
		Path:             "short",
		DirectPointIDs:   []string{},
	})

	trPos, _ := store.GetTrace("t-pos")
	svc.SubmitFeedback(trPos, FeedbackRequest{Type: "positive"})

	events, _ := store.ListLearningEvents("user_correction", 0, 20)
	if len(events) != 0 {
		t.Errorf("positive feedback should not create event, got %d", len(events))
	}
}
