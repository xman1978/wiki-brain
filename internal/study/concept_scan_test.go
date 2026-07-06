package study

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/concept"
)

func testConceptConfig() concept.Config {
	return concept.Config{
		AddEventMin:       3,
		AddDistinctMin:    2,
		AddOverlapMin:     0.5,
		MergeCooccurMin:   3,
		MergeOverlapMin:   0.5,
		CandidateIdleDays: 60,
		EventWindowDays:   90,
	}
}

// seedKUNoConcept mirrors seedKU (store_test.go) but leaves concept_id NULL
// — required for concept-gap add-cluster scans, since seedKU always sets one.
func seedKUNoConcept(t *testing.T, db *sql.DB, unitID, sourceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, concept_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, NULL, 'topic', 1, 10, 'completed', 'v1')`, unitID, sourceID); err != nil {
		t.Fatalf("seed KU (no concept): %v", err)
	}
}

// seedConceptGapEvent extends seedActivationGapEvent's payload with
// gap_level=concept_gap (docs/impl/v1/concept-evolution.md activation_gap
// payload 扩展).
func seedConceptGapEvent(t *testing.T, db *sql.DB, eventID, traceID string, directPointIDs []string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"question_terms": "terms", "direct_point_ids": directPointIDs,
		"gap_level": "concept_gap", "null_concept_ratio": 1.0,
	})
	seedLearningEvent(t, db, eventID, traceID, "activation_gap", string(payload))
}

func TestRun_WithoutConceptSvc_ReportSectionEmpty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig(), newTestActivationSvc(db), nil, 0)

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	content, _ := store.GetReport(result.ReportID)
	var report Report
	if err := json.Unmarshal([]byte(*content), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.ConceptCandidates.AddCreated != 0 || len(report.ConceptCandidates.PendingAdd) != 0 {
		t.Errorf("expected empty concept_candidates section without a wired conceptSvc, got %+v", report.ConceptCandidates)
	}
}

func TestRun_WithConceptSvc_ScansAndReportsPendingCandidates(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig(), newTestActivationSvc(db), nil, 0)

	conceptStore := concept.NewStore(db)
	conceptSvc := concept.NewService(conceptStore, testConceptConfig(), nil)
	svc.SetConceptSvc(conceptSvc)

	// Seed a concept_gap event cluster that meets the add-candidate
	// thresholds (docs/impl/v1/concept-evolution.md 步骤 2): 3 events, same
	// KP, each on its own trace (-> 3 distinct question_hash automatically,
	// since seedTrace derives hash from trace_id).
	seedSource(t, db, "src1")
	seedKUNoConcept(t, db, "ku1", "src1")
	seedKP(t, db, "kp1", "ku1", "src1", "知识点1")
	for _, suffix := range []string{"1", "2", "3"} {
		answerID, traceID, eventID := "a-"+suffix, "t-"+suffix, "evt-"+suffix
		seedAnswer(t, db, answerID)
		seedTrace(t, db, traceID, answerID, "q", "terms", "confident", "full", []string{"kp1"})
		seedConceptGapEvent(t, db, eventID, traceID, []string{"kp1"})
	}

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	content, _ := store.GetReport(result.ReportID)
	var report Report
	if err := json.Unmarshal([]byte(*content), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.ConceptCandidates.AddCreated != 1 {
		t.Errorf("expected 1 add candidate created, got %+v", report.ConceptCandidates)
	}
	if len(report.ConceptCandidates.PendingAdd) != 1 {
		t.Fatalf("expected 1 pending add candidate in report, got %d", len(report.ConceptCandidates.PendingAdd))
	}
	if report.ConceptCandidates.ConceptGapEventCount != 3 {
		t.Errorf("expected concept_gap_event_count=3, got %d", report.ConceptCandidates.ConceptGapEventCount)
	}
}
