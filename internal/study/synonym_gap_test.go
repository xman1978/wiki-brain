package study

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func testConfigWithSynonym(min, distinctMin int, autoPromote bool) config.StudyConfig {
	cfg := testConfig()
	cfg.SynonymGapMin = min
	cfg.SynonymGapDistinctMin = distinctMin
	cfg.SynonymAutoPromote = autoPromote
	return cfg
}

func seedSynonymGapEvent(t *testing.T, db *sql.DB, eventID, traceID, pointID, linkID, querySubject, observedSubject string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"point_id":         pointID,
		"link_id":          linkID,
		"query_subject":    querySubject,
		"observed_subject": observedSubject,
		"question_terms":   querySubject,
	})
	seedLearningEvent(t, db, eventID, traceID, "subject_synonym_gap", string(payload))
}

func TestAggregateSynonymCandidates_CreatesPendingConfirmWhenThresholdMet(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfigWithSynonym(2, 2, false), activationSvc, nil, 0, 0)

	seedAnswer(t, db, "a1")
	seedAnswer(t, db, "a2")
	seedTrace(t, db, "t1", "a1", "q1", "差旅报销", "confident", "short", nil)
	seedTrace(t, db, "t2", "a2", "q2", "差旅报销", "confident", "short", nil)
	seedSynonymGapEvent(t, db, "e1", "t1", "kp1", "link1", "差旅报销", "招待费报销")
	seedSynonymGapEvent(t, db, "e2", "t2", "kp1", "link1", "差旅报销", "招待费报销")

	var actions LearningActionsSummary
	if err := svc.aggregateAndCreateSynonymCandidates(&actions); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if actions.SynonymCandidatesCreated != 1 {
		t.Fatalf("expected 1 synonym candidate created, got %d", actions.SynonymCandidatesCreated)
	}

	syns, err := activationSvc.ListSynonyms(activation.ListSynonymsFilter{})
	if err != nil {
		t.Fatalf("list synonyms: %v", err)
	}
	if len(syns) != 1 {
		t.Fatalf("expected 1 synonym row, got %d", len(syns))
	}
	syn := syns[0]
	if syn.Status != "candidate" {
		t.Errorf("status = %q, want candidate", syn.Status)
	}
	if syn.Term != "差旅报销" || syn.Canonical != "招待费报销" {
		t.Errorf("term=%q canonical=%q, want term=差旅报销 canonical=招待费报销 (observed side wins)", syn.Term, syn.Canonical)
	}

	// Both source events should now be marked processed.
	var unprocessed int
	db.QueryRow(`SELECT COUNT(*) FROM learning_events WHERE event_type = 'subject_synonym_gap' AND processed = 0`).Scan(&unprocessed)
	if unprocessed != 0 {
		t.Errorf("expected all subject_synonym_gap events processed, got %d unprocessed", unprocessed)
	}
}

func TestAggregateSynonymCandidates_BelowThreshold_NoCandidate(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfigWithSynonym(3, 2, false), activationSvc, nil, 0, 0)

	seedAnswer(t, db, "a1")
	seedTrace(t, db, "t1", "a1", "q1", "差旅报销", "confident", "short", nil)
	seedSynonymGapEvent(t, db, "e1", "t1", "kp1", "link1", "差旅报销", "招待费报销")

	var actions LearningActionsSummary
	if err := svc.aggregateAndCreateSynonymCandidates(&actions); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if actions.SynonymCandidatesCreated != 0 {
		t.Errorf("expected 0 candidates below threshold, got %d", actions.SynonymCandidatesCreated)
	}
}

func TestAggregateSynonymCandidates_AutoPromote_CreatesActiveDirectly(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfigWithSynonym(2, 2, true), activationSvc, nil, 0, 0)

	seedAnswer(t, db, "a1")
	seedAnswer(t, db, "a2")
	seedTrace(t, db, "t1", "a1", "q1", "差旅报销", "confident", "short", nil)
	seedTrace(t, db, "t2", "a2", "q2", "差旅报销", "confident", "short", nil)
	seedSynonymGapEvent(t, db, "e1", "t1", "kp1", "link1", "差旅报销", "招待费报销")
	seedSynonymGapEvent(t, db, "e2", "t2", "kp1", "link1", "差旅报销", "招待费报销")

	var actions LearningActionsSummary
	if err := svc.aggregateAndCreateSynonymCandidates(&actions); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	syns, _ := activationSvc.ListSynonyms(activation.ListSynonymsFilter{})
	if len(syns) != 1 || syns[0].Status != "active" {
		t.Fatalf("expected 1 active synonym, got %+v", syns)
	}
}

func TestAggregateSynonymCandidates_DedupSkipsExistingTerm(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, testConfigWithSynonym(1, 1, false), activationSvc, nil, 0, 0)

	if _, err := activationSvc.CreateActiveSynonym("", "差旅报销", "招待费报销", nil); err != nil {
		t.Fatalf("seed existing synonym: %v", err)
	}

	seedAnswer(t, db, "a1")
	seedTrace(t, db, "t1", "a1", "q1", "差旅报销", "confident", "short", nil)
	seedSynonymGapEvent(t, db, "e1", "t1", "kp1", "link1", "差旅报销", "招待费报销")

	var actions LearningActionsSummary
	if err := svc.aggregateAndCreateSynonymCandidates(&actions); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if actions.SynonymCandidatesCreated != 0 {
		t.Errorf("expected no new candidate for an already-covered term, got %d", actions.SynonymCandidatesCreated)
	}

	syns, _ := activationSvc.ListSynonyms(activation.ListSynonymsFilter{})
	if len(syns) != 1 {
		t.Errorf("expected still exactly 1 synonym row, got %d", len(syns))
	}
}
