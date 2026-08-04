package entry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

func seedDomain(t *testing.T, db *sql.DB, domainID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO domains (domain_id, name) VALUES (?, ?)`, domainID, domainID); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
}

func seedEntry(t *testing.T, db *sql.DB, conceptID, domainID string, mergedInto sql.NullString) {
	t.Helper()
	seedDomain(t, db, domainID)
	if _, err := db.Exec(`INSERT INTO entries (entry_id, domain_id, name, merged_into) VALUES (?, ?, ?, ?)`,
		conceptID, domainID, conceptID, mergedInto); err != nil {
		t.Fatalf("seed concept: %v", err)
	}
}

func seedSource(t *testing.T, db *sql.DB, sourceID, domainID string) {
	t.Helper()
	seedDomain(t, db, domainID)
	if _, err := db.Exec(`INSERT OR IGNORE INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES (?, 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'completed', ?)`, sourceID, domainID); err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func seedKU(t *testing.T, db *sql.DB, unitID, sourceID, center string, conceptID sql.NullString) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, entry_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, ?, ?, 1, 10, 'completed', 'v1')`, unitID, sourceID, conceptID, center); err != nil {
		t.Fatalf("seed KU: %v", err)
	}
}

func seedKP(t *testing.T, db *sql.DB, pointID, unitID, sourceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES (?, ?, ?, 'test content', 'fact')`, pointID, unitID, sourceID); err != nil {
		t.Fatalf("seed KP: %v", err)
	}
}

func seedAnswer(t *testing.T, db *sql.DB, answerID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name)
		VALUES (?, 'q', 'c', 'short', 'v1', 'default')`, answerID); err != nil {
		t.Fatalf("seed answer: %v", err)
	}
}

// seedTrace inserts a confident trace with the given direct_point_ids, and
// backdates created_at by ageDays (0 = now) for window-boundary tests.
func seedTrace(t *testing.T, db *sql.DB, traceID, questionHash string, pointIDs []string, ageDays int) {
	t.Helper()
	answerID := "a-" + traceID
	seedAnswer(t, db, answerID)
	pointIDsJSON, err := json.Marshal(pointIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids)
		VALUES (?, ?, 'q', ?, 'q terms', 'confident', 'short', ?)`,
		traceID, answerID, questionHash, string(pointIDsJSON)); err != nil {
		t.Fatalf("seed trace: %v", err)
	}
	if ageDays > 0 {
		if _, err := db.Exec(`UPDATE traces SET created_at = datetime('now', '-' || ? || ' days') WHERE trace_id = ?`, ageDays, traceID); err != nil {
			t.Fatalf("backdate trace: %v", err)
		}
	}
}

// seedEntryGapEvent inserts an activation_gap learning_event classified as
// gapLevel, backing trace created fresh (question_hash = questionHash).
func seedEntryGapEvent(t *testing.T, db *sql.DB, eventID, questionHash string, pointIDs []string, gapLevel string) string {
	t.Helper()
	traceID := "t-" + eventID
	seedTrace(t, db, traceID, questionHash, pointIDs, 0)

	payload := map[string]interface{}{
		"question_terms":     "q terms",
		"direct_point_ids":   pointIDs,
		"gap_level":          gapLevel,
		"null_entry_ratio": 1.0,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO learning_events (event_id, trace_id, event_type, payload) VALUES (?, ?, 'activation_gap', ?)`,
		eventID, traceID, string(b)); err != nil {
		t.Fatalf("seed concept gap event: %v", err)
	}
	return traceID
}

func testConfig() Config {
	return Config{
		AddEventMin:       3,
		AddDistinctMin:    2,
		AddOverlapMin:     0.5,
		MergeCooccurMin:   3,
		MergeOverlapMin:   0.5,
		CandidateIdleDays: 60,
		EventWindowDays:   90,
	}
}

func candidateEvidence(t *testing.T, c *CandidateRow) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(c.Evidence), &m); err != nil {
		t.Fatalf("decode evidence %q: %v", c.Evidence, err)
	}
	return m
}

func candidatePointIDs(t *testing.T, c *CandidateRow) []string {
	t.Helper()
	var ids []string
	if err := json.Unmarshal([]byte(c.PointIDs), &ids); err != nil {
		t.Fatalf("decode point_ids %q: %v", c.PointIDs, err)
	}
	return ids
}

func candidateEventIDs(t *testing.T, c *CandidateRow) []string {
	t.Helper()
	var ids []string
	if err := json.Unmarshal([]byte(c.EventIDs), &ids); err != nil {
		t.Fatalf("decode event_ids %q: %v", c.EventIDs, err)
	}
	return ids
}

func candidateMergeFrom(t *testing.T, c *CandidateRow) []string {
	t.Helper()
	var ids []string
	if err := json.Unmarshal([]byte(c.MergeFrom), &ids); err != nil {
		t.Fatalf("decode merge_from %q: %v", c.MergeFrom, err)
	}
	return ids
}

func newEventID() string {
	return fmt.Sprintf("evt-%s", uuid.New().String())
}

// setupServiceWithWiki wires a real wiki.Service (fake LLM, real Bleve index
// in a temp dir, no real network calls) so merge confirm's needs_recompile
// flagging can be exercised end-to-end (docs/impl/v1/concept-evolution.md
// 步骤 3).
func setupServiceWithWiki(t *testing.T) (*Service, *Store, *sql.DB, *wiki.Service) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	tmpDir := foundation.NewTestDir(t)
	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	wikiStore := wiki.NewStore(db)
	fake := llm.NewFakeClient()
	wikiSvc := wiki.NewService(wikiStore, fake, idxMgr.Wiki, idxMgr.Points, config.WikiConfig{CompileMaxChars: 12000, RecompileNewKPMin: 2}, 0)

	svc := NewService(store, testConfig(), wikiSvc)
	return svc, store, db, wikiSvc
}

func insertWikiPage(t *testing.T, db *sql.DB, pageID, conceptID string) {
	t.Helper()
	wikiStore := wiki.NewStore(db)
	err := wikiStore.InsertPage(&wiki.Page{
		PageID:        pageID,
		PageType:      wiki.PageTypeConcept,
		EntryID:     sql.NullString{String: conceptID, Valid: true},
		Title:         "test page " + pageID,
		Content:       "## 稳定结论\ntest\n",
		Status:        wiki.StatusDraft,
		PromptVersion: "v1",
		ModelName:     "default",
	})
	if err != nil {
		t.Fatalf("insert wiki page: %v", err)
	}
}
