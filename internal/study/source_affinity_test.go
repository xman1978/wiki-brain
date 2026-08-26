package study

import (
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// TestEvictIdleSourceAffinity_CleansOldRows exercises the wiring end to end:
// Study.evictIdle -> retrieval.Service.CleanIdleSubjectNorms/
// CleanIdleSourceAffinity -> the underlying DELETE queries, using a real
// SQLite DB rather than a mock so the SQL itself is under test too.
func TestEvictIdleSourceAffinity_CleansOldRows(t *testing.T) {
	db := setupTestDB(t)
	retrievalStore := retrieval.NewStore(db)
	retrievalSvc := retrieval.NewService(retrievalStore, nil, nil, nil, &config.Config{}, nil, nil, nil)

	old := time.Now().UTC().AddDate(0, 0, -30)
	if _, err := db.Exec(`INSERT INTO subject_norms (norm_id, domain_id, subject, last_hit_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		"n1", "d1", "报销", old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_affinity (affinity_id, domain_id, subject_norm, source_id, consecutive_failures, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`,
		"a1", "d1", "报销", "s1", old, old); err != nil {
		t.Fatal(err)
	}

	studyStore := NewStore(db)
	svc := NewService(studyStore, config.StudyConfig{}, nil, nil, 0, 0, 0, false, 0, 0)
	svc.SetSourceAffinityCleanup(retrievalSvc, 7)

	if err := svc.evictIdle(&LearningActionsSummary{}); err != nil {
		t.Fatal(err)
	}

	var subjectCount, affinityCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subject_norms`).Scan(&subjectCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM source_affinity`).Scan(&affinityCount); err != nil {
		t.Fatal(err)
	}
	if subjectCount != 0 {
		t.Errorf("expected subject_norms cleaned, got %d rows left", subjectCount)
	}
	if affinityCount != 0 {
		t.Errorf("expected source_affinity cleaned, got %d rows left", affinityCount)
	}
}

// TestEvictIdleSourceAffinity_Unwired_NoOp asserts evictIdle doesn't panic or
// error when SetSourceAffinityCleanup was never called (feature unwired).
func TestEvictIdleSourceAffinity_Unwired_NoOp(t *testing.T) {
	db := setupTestDB(t)
	studyStore := NewStore(db)
	svc := NewService(studyStore, config.StudyConfig{}, nil, nil, 0, 0, 0, false, 0, 0)

	if err := svc.evictIdle(&LearningActionsSummary{}); err != nil {
		t.Fatal(err)
	}
}
