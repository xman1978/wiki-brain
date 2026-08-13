package trace

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// fakeSynonymEnricher implements ObservedConditionEnricher for testing
// detectSubjectSynonymGaps in isolation from the real activation package
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
type fakeSynonymEnricher struct {
	gapLinkID   string
	gapObserved string
	gapOK       bool

	enrichCalls   int
	gapCalls      int
	gapCallPoints []string

	recordOutcomeCalls      []recordOutcomeCall
	recordAuditOutcomeCalls []recordAuditOutcomeCall
	recordOutcomeErr        error
	recordAuditOutcomeErr   error
}

// recordOutcomeCall/recordAuditOutcomeCall capture every RecordOutcome /
// RecordAuditOutcome invocation the fake enricher receives, so tests can
// assert on exact call count and arguments (docs/impl/v1/trace.md 完成标准).
type recordOutcomeCall struct {
	linkID, subject, intent, audience, constraint string
	success                                       bool
	questionTerms                                 string
}

type recordAuditOutcomeCall struct {
	linkID, subject, intent, audience, constraint string
	agree                                         bool
}

func (f *fakeSynonymEnricher) EnrichFromConfidentFullPath(pointIDs []string, subject, intent, audience, constraint, questionTerms string, max int) error {
	f.enrichCalls++
	return nil
}

func (f *fakeSynonymEnricher) FindSynonymGapCandidate(pointID, subject, intent, audience, constraint string) (string, string, bool, error) {
	f.gapCalls++
	f.gapCallPoints = append(f.gapCallPoints, pointID)
	return f.gapLinkID, f.gapObserved, f.gapOK, nil
}

func (f *fakeSynonymEnricher) RecordOutcome(linkID, subject, intent, audience, constraint string, success bool, questionTerms string) error {
	f.recordOutcomeCalls = append(f.recordOutcomeCalls, recordOutcomeCall{linkID, subject, intent, audience, constraint, success, questionTerms})
	return f.recordOutcomeErr
}

func (f *fakeSynonymEnricher) RecordAuditOutcome(linkID, subject, intent, audience, constraint string, agree bool) error {
	f.recordAuditOutcomeCalls = append(f.recordAuditOutcomeCalls, recordAuditOutcomeCall{linkID, subject, intent, audience, constraint, agree})
	return f.recordAuditOutcomeErr
}

func TestProcessTrace_FullPath_Confident_SubjectSynonymGapEventRecorded(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-syn-1")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{gapLinkID: "link1", gapObserved: "招待费报销", gapOK: true}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-syn-1",
		Question:  "差旅报销怎么处理",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			PathType:       retrieval.PathTypeFull,
			ActivationHits: []retrieval.ActivationHit{},
			DirectEvidence: []retrieval.Evidence{{FactID: "f1", PointID: "p1"}},
			Subject:        "差旅报销",
		},
	}
	svc.ProcessTrace(r)

	if fake.gapCalls != 1 || len(fake.gapCallPoints) != 1 || fake.gapCallPoints[0] != "p1" {
		t.Fatalf("expected FindSynonymGapCandidate called once with p1, got calls=%d points=%v", fake.gapCalls, fake.gapCallPoints)
	}
	if fake.enrichCalls != 1 {
		t.Errorf("expected EnrichFromConfidentFullPath still called once, got %d", fake.enrichCalls)
	}

	events, err := store.ListLearningEvents("subject_synonym_gap", 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 subject_synonym_gap event, got %d (err=%v)", len(events), err)
	}
	payload := decodePayload(t, events[0].Payload)
	if payload["point_id"] != "p1" || payload["link_id"] != "link1" {
		t.Errorf("unexpected payload: %+v", payload)
	}
	if payload["observed_subject"] != "招待费报销" {
		t.Errorf("observed_subject = %v, want 招待费报销", payload["observed_subject"])
	}
	if payload["question_terms"] == nil {
		t.Errorf("expected question_terms in payload, got %+v", payload)
	}
}

func TestProcessTrace_FullPath_Confident_NoSynonymGapEventWhenNotFound(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-syn-2")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{gapOK: false}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-syn-2",
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

	events, err := store.ListLearningEvents("subject_synonym_gap", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("expected no subject_synonym_gap event, got %d (err=%v)", len(events), err)
	}
}

func TestProcessTrace_WikiPath_NoSynonymGapDetection(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-syn-3")
	insertTestKP(t, db, "p1")

	fake := &fakeSynonymEnricher{gapLinkID: "link1", gapObserved: "x", gapOK: true}
	svc.SetObservedConditionEnricher(fake, 50)

	r := &answer.AnswerResult{
		AnswerID:  "a-syn-3",
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

	if fake.gapCalls != 0 {
		t.Errorf("expected no gap detection calls for wiki path, got %d", fake.gapCalls)
	}
	events, err := store.ListLearningEvents("subject_synonym_gap", 0, 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("expected no subject_synonym_gap event for wiki path, got %d (err=%v)", len(events), err)
	}
}
