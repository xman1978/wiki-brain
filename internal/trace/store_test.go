package trace

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func insertTestAnswer(t *testing.T, db *sql.DB, answerID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name) VALUES (?, 'q', 'c', 'short', 'v1', 'default')`, answerID)
	if err != nil {
		t.Fatalf("insert test answer: %v", err)
	}
}

func insertTestKP(t *testing.T, db *sql.DB, pointID string) {
	t.Helper()
	db.Exec(`INSERT OR IGNORE INTO sources (source_id, title, format, file_name, original_path, markdown_path, status) VALUES ('s-test', 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'completed')`)
	db.Exec(`INSERT OR IGNORE INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version) VALUES ('u-test', 's-test', 'test', 1, 10, 'completed', 'v1')`)
	_, err := db.Exec(`INSERT OR IGNORE INTO knowledge_points (point_id, unit_id, source_id, content, point_type) VALUES (?, 'u-test', 's-test', 'test', 'fact')`, pointID)
	if err != nil {
		t.Fatalf("insert test kp: %v", err)
	}
}

func TestStore_SaveAndGetTrace(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	insertTestAnswer(t, db, "a-001")

	tr := &Trace{
		TraceID:          "t-001",
		AnswerID:         "a-001",
		Question:         "测试问题",
		QuestionHash:     "abc123",
		QuestionTerms:    "测试 问题",
		RetrievalQuality: QualityConfident,
		Path:             "short",
		DirectPointIDs:   []string{"p1", "p2"},
	}

	if err := store.SaveTrace(tr); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	got, err := store.GetTrace("t-001")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil trace")
	}
	if got.RetrievalQuality != QualityConfident {
		t.Errorf("quality: got %q", got.RetrievalQuality)
	}
	if len(got.DirectPointIDs) != 2 {
		t.Errorf("direct_point_ids: got %v", got.DirectPointIDs)
	}
	if got.Path != "short" {
		t.Errorf("path: got %q", got.Path)
	}
}

func TestStore_GetTraceNotFound(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	got, err := store.GetTrace("nonexistent")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent")
	}
}

func TestStore_ListTraces(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	for _, q := range []string{QualityConfident, QualityPartial, QualityGap} {
		insertTestAnswer(t, db, "a-"+q)
		store.SaveTrace(&Trace{
			TraceID:          "t-" + q,
			AnswerID:         "a-" + q,
			Question:         "q",
			QuestionHash:     "h",
			QuestionTerms:    "t",
			RetrievalQuality: q,
			Path:             "short",
			DirectPointIDs:   []string{},
		})
	}

	all, err := store.ListTraces("", "", 20, 0)
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	confident, err := store.ListTraces(QualityConfident, "", 20, 0)
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(confident) != 1 {
		t.Errorf("expected 1 confident, got %d", len(confident))
	}
}

func TestStore_UpdateFeedback(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
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

	if err := store.UpdateFeedback("t-fb", "negative", "wrong answer"); err != nil {
		t.Fatalf("UpdateFeedback: %v", err)
	}

	got, _ := store.GetTrace("t-fb")
	if !got.HasFeedback {
		t.Error("expected has_feedback=true")
	}
	if got.FeedbackType != "negative" {
		t.Errorf("feedback_type: got %q", got.FeedbackType)
	}
}

func TestStore_CooccurrenceDedup(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	insertTestKP(t, db, "p1")

	// First call should create entries
	err := store.UpdateCooccurrence("hash1", "terms1", []string{"p1"}, QualityConfident)
	if err != nil {
		t.Fatalf("first UpdateCooccurrence: %v", err)
	}

	coocs, _ := store.ListCooccurrence("p1", 0, 50)
	if len(coocs) != 1 {
		t.Fatalf("expected 1 cooccurrence, got %d", len(coocs))
	}
	if coocs[0].HitCount != 1 || coocs[0].ConfidentCount != 1 {
		t.Errorf("expected hit=1 confident=1, got hit=%d confident=%d", coocs[0].HitCount, coocs[0].ConfidentCount)
	}

	// Same question_hash should be deduped
	err = store.UpdateCooccurrence("hash1", "terms1", []string{"p1"}, QualityConfident)
	if err != nil {
		t.Fatalf("second UpdateCooccurrence: %v", err)
	}

	coocs, _ = store.ListCooccurrence("p1", 0, 50)
	if coocs[0].HitCount != 1 {
		t.Errorf("dedup failed: hit_count should still be 1, got %d", coocs[0].HitCount)
	}

	// Different question_hash should increment
	err = store.UpdateCooccurrence("hash2", "terms1", []string{"p1"}, QualityPartial)
	if err != nil {
		t.Fatalf("third UpdateCooccurrence: %v", err)
	}

	coocs, _ = store.ListCooccurrence("p1", 0, 50)
	if coocs[0].HitCount != 2 {
		t.Errorf("expected hit_count=2, got %d", coocs[0].HitCount)
	}
	if coocs[0].ConfidentCount != 1 {
		t.Errorf("partial should not increment confident_count, got %d", coocs[0].ConfidentCount)
	}
}

func TestStore_LearningEvents(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	insertTestAnswer(t, db, "a-le")

	store.SaveTrace(&Trace{
		TraceID:          "t-le",
		AnswerID:         "a-le",
		Question:         "q",
		QuestionHash:     "h",
		QuestionTerms:    "t",
		RetrievalQuality: QualityGap,
		Path:             "none",
		DirectPointIDs:   []string{},
	})

	if err := store.SaveLearningEvent("t-le", "knowledge_gap", `{"question":"q"}`); err != nil {
		t.Fatalf("SaveLearningEvent: %v", err)
	}

	events, err := store.ListLearningEvents("knowledge_gap", 0, 20)
	if err != nil {
		t.Fatalf("ListLearningEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != "knowledge_gap" {
		t.Errorf("event_type: got %q", events[0].EventType)
	}
}
