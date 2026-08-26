package trace

import (
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

type fakeSourceAffinityWriter struct {
	calls []struct {
		subject   string
		sourceIDs []string
	}
	feedbackFailureCalls []struct {
		subject   string
		sourceIDs []string
	}
}

func (f *fakeSourceAffinityWriter) RecordSourceAffinityOutcome(subject string, sourceIDs []string) error {
	f.calls = append(f.calls, struct {
		subject   string
		sourceIDs []string
	}{subject, sourceIDs})
	return nil
}

func (f *fakeSourceAffinityWriter) RecordSourceAffinityFeedbackFailure(subject string, sourceIDs []string) error {
	f.feedbackFailureCalls = append(f.feedbackFailureCalls, struct {
		subject   string
		sourceIDs []string
	}{subject, sourceIDs})
	return nil
}

func mustSourceRef(t *testing.T, sourceID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(retrieval.SourceRef{SourceID: sourceID, LineStart: 1, LineEnd: 10})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRecordSourceAffinity_FullPathConfident_WritesOutcome asserts a
// full-path confident trace whose direct evidence resolves to a source_id
// calls RecordSourceAffinityOutcome with that source — the only place a
// source_affinity binding gets created (see recordSourceAffinity's comment).
func TestRecordSourceAffinity_FullPathConfident_WritesOutcome(t *testing.T) {
	svc, _, db := setupService(t)
	writer := &fakeSourceAffinityWriter{}
	svc.SetSourceAffinityWriter(writer)
	insertTestAnswer(t, db, "a-affinity-1")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-affinity-1",
		Question:  "报销流程是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "full",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFull,
			Subject:  "报销",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1", SourceRef: mustSourceRef(t, "s1")},
			},
		},
	}
	svc.ProcessTrace(r)

	if len(writer.calls) != 1 {
		t.Fatalf("expected 1 RecordSourceAffinityOutcome call, got %d", len(writer.calls))
	}
	if writer.calls[0].subject != "报销" {
		t.Errorf("subject = %q, want 报销", writer.calls[0].subject)
	}
	if len(writer.calls[0].sourceIDs) != 1 || writer.calls[0].sourceIDs[0] != "s1" {
		t.Errorf("sourceIDs = %v, want [s1]", writer.calls[0].sourceIDs)
	}
}

// TestRecordSourceAffinity_FastPath_NoWrite asserts the fast path (not full)
// never triggers a write — the shortcut only reads/writes around the slow
// path's own domain/source filter steps, so a fast-path hit has nothing to
// reinforce here.
func TestRecordSourceAffinity_FastPath_NoWrite(t *testing.T) {
	svc, _, db := setupService(t)
	writer := &fakeSourceAffinityWriter{}
	svc.SetSourceAffinityWriter(writer)
	insertTestAnswer(t, db, "a-affinity-2")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-affinity-2",
		Question:  "报销流程是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFast,
			Subject:  "报销",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1", SourceRef: mustSourceRef(t, "s1")},
			},
		},
	}
	svc.ProcessTrace(r)

	if len(writer.calls) != 0 {
		t.Fatalf("expected no RecordSourceAffinityOutcome call on fast path, got %d", len(writer.calls))
	}
}

// TestSubmitFeedback_Negative_RecordsSourceAffinityFailure asserts that
// negative feedback on a full-path confident trace (whose source_affinity
// source_ids were persisted by recordSourceAffinity) calls
// RecordSourceAffinityFeedbackFailure with that trace's subject/source_ids —
// the second input to the eviction mechanism alongside the shortcut's own
// empty-recall failure (会话讨论 2026-08-26). Absence of feedback must not
// call it at all, and "positive"/"correction" feedback must not either.
func TestSubmitFeedback_Negative_RecordsSourceAffinityFailure(t *testing.T) {
	svc, _, db := setupService(t)
	writer := &fakeSourceAffinityWriter{}
	svc.SetSourceAffinityWriter(writer)
	insertTestAnswer(t, db, "a-affinity-3")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-affinity-3",
		Question:  "报销流程是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "full",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFull,
			Subject:  "报销",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1", SourceRef: mustSourceRef(t, "s1")},
			},
		},
	}
	svc.ProcessTrace(r)

	traces, err := svc.store.ListTraces("", r.AnswerID, "", 20, 0)
	if err != nil || len(traces) != 1 {
		t.Fatalf("ListTraces: %v, %d traces", err, len(traces))
	}
	traceRow, err := svc.store.GetTrace(traces[0].TraceID)
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if len(traceRow.SourceAffinitySourceIDs) != 1 || traceRow.SourceAffinitySourceIDs[0] != "s1" {
		t.Fatalf("persisted source_affinity_source_ids = %v, want [s1]", traceRow.SourceAffinitySourceIDs)
	}

	if err := svc.SubmitFeedback(traceRow, FeedbackRequest{Type: "negative"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	if len(writer.feedbackFailureCalls) != 1 {
		t.Fatalf("expected 1 RecordSourceAffinityFeedbackFailure call, got %d", len(writer.feedbackFailureCalls))
	}
	if writer.feedbackFailureCalls[0].subject != "报销" {
		t.Errorf("subject = %q, want 报销", writer.feedbackFailureCalls[0].subject)
	}
	if len(writer.feedbackFailureCalls[0].sourceIDs) != 1 || writer.feedbackFailureCalls[0].sourceIDs[0] != "s1" {
		t.Errorf("sourceIDs = %v, want [s1]", writer.feedbackFailureCalls[0].sourceIDs)
	}
}

// TestSubmitFeedback_Positive_NoSourceAffinityWrite asserts positive feedback
// never calls RecordSourceAffinityFeedbackFailure — absence of negative
// feedback must be ignored, not treated as reinforcement or failure.
func TestSubmitFeedback_Positive_NoSourceAffinityWrite(t *testing.T) {
	svc, _, db := setupService(t)
	writer := &fakeSourceAffinityWriter{}
	svc.SetSourceAffinityWriter(writer)
	insertTestAnswer(t, db, "a-affinity-4")
	insertTestKP(t, db, "p1")

	r := &answer.AnswerResult{
		AnswerID:  "a-affinity-4",
		Question:  "报销流程是什么",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "full",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType: retrieval.PathTypeFull,
			Subject:  "报销",
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1", SourceRef: mustSourceRef(t, "s1")},
			},
		},
	}
	svc.ProcessTrace(r)

	traces, err := svc.store.ListTraces("", r.AnswerID, "", 20, 0)
	if err != nil || len(traces) != 1 {
		t.Fatalf("ListTraces: %v, %d traces", err, len(traces))
	}
	traceRow, err := svc.store.GetTrace(traces[0].TraceID)
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}

	if err := svc.SubmitFeedback(traceRow, FeedbackRequest{Type: "positive"}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	if len(writer.feedbackFailureCalls) != 0 {
		t.Fatalf("expected no RecordSourceAffinityFeedbackFailure call on positive feedback, got %d", len(writer.feedbackFailureCalls))
	}
}
