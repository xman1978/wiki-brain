package answer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// setupFallbackTestService wires a full stack (real retrieval.Service +
// activation.Service on the same DB) so docs/impl/v1/retrieval.md 步骤 6b can
// be exercised end-to-end: a verified ActivationLink matches the question,
// producing a fast-path EvidenceSet; the slow path deliberately finds no FTS
// candidates (Chinese question vs. no seeded content match) so it resolves
// via the "merged==0" early return without needing a rerank_extract.md fake
// response — enough to prove the persistence/path_type/hits mechanics
// without needing the regenerated answer to be substantively different.
func setupFallbackTestService(t *testing.T, fastPathFallback bool) (*Service, *sql.DB, string) {
	t.Helper()
	db := foundation.NewTestDB(t)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('s1', 'Algebra', 'md', 'algebra.md', '/tmp/algebra.md', '/tmp/algebra.md', 'completed')`)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u1', 's1', 'linear equations', 1, 3, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p1', 'u1', 's1', 'ax+b=0 is linear', 'fact')`)

	tmpDir := foundation.NewTestDir(t)
	mdPath := filepath.Join(tmpDir, "algebra.md")
	os.WriteFile(mdPath, []byte("# Algebra\nLinear equations ax+b=0\nLine 3"), 0644)
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's1'`, mdPath)

	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })
	idxMgr.Units.Index("u1", map[string]interface{}{
		"unit_id": "u1", "source_id": "s1", "center": "linear equations",
		"line_start": 1, "line_end": 3, "content": "Linear equations ax+b=0", "lifecycle": "current",
	})
	idxMgr.Points.Index("p1", map[string]interface{}{
		"point_id": "p1", "unit_id": "u1", "source_id": "s1",
		"content": "ax+b=0 is linear", "point_type": "fact", "lifecycle": "current",
	})

	fake := llm.NewFakeClient()
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"source_ids": ["s1"]}`})
	fake.SetResponse("answer_short.md", llm.FakeResponse{Output: `{"content":"some answer","citations":[]}`})

	cfg := &config.Config{
		Retrieval: config.RetrievalConfig{
			OutlineFTSMinScore:         0.5,
			RerankTopN:                 20,
			ActivationMatchMin:         0.7,
			ActivationMatchMinFallback: 0.85,
			ActivationMatchTop:         5,
			FastPath:                   true,
			FastPathFallback:           fastPathFallback,
		},
	}

	activationStore := activation.NewStore(db)
	activationSvc := activation.NewService(activationStore, activation.NewMatcher(activationStore))

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	link, err := activationSvc.CreateLink(qTerms, activation.LinkCondition{}, "p1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify link: %v", err)
	}

	retStore := retrieval.NewStore(db)
	retSvc := retrieval.NewService(retStore, fake, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg, activationSvc, nil, nil)

	store := NewStore(db)
	q := queue.New(10)
	tracedCount := 0
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) { tracedCount++ })
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, retSvc)

	return svc, db, question
}

func TestAnswerFromQuestion_FastPathFailure_FallsBackToSlowPath(t *testing.T) {
	svc, db, question := setupFallbackTestService(t, true)

	result, err := svc.AnswerFromQuestion(context.Background(), question, false)
	if err != nil {
		t.Fatalf("AnswerFromQuestion: %v", err)
	}

	if result.EvidenceSet.PathType != retrieval.PathTypeFull {
		t.Errorf("path_type = %q, want full (fallback should have happened)", result.EvidenceSet.PathType)
	}
	if len(result.EvidenceSet.ActivationHits) != 1 || result.EvidenceSet.ActivationHits[0].PointID != "p1" {
		t.Fatalf("expected original activation_hits preserved on fallback ES, got %+v", result.EvidenceSet.ActivationHits)
	}

	// Exactly one answer row persisted — the discarded fast-path attempt must
	// never have been saved (docs/impl/v1/retrieval.md 步骤 6b, "本次 trace").
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM answers`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 persisted answer, got %d", count)
	}

	saved, err := svc.store.Get(result.AnswerID)
	if err != nil || saved == nil {
		t.Fatalf("expected the returned answer_id to be the persisted one, err=%v", err)
	}
}

// maybeFallbackToSlowPath's own decision logic, tested directly (package
// answer white-box test) rather than through a full AnswerFromQuestion round
// trip — a real fast-path success can't be simulated with FakeClient because
// the "good" fact_id is a uuid generated at evidence-build time that no
// static FakeResponse can reference, and validateCitations would strip any
// citation that doesn't match a real fact_id in the EvidenceSet.
func TestMaybeFallbackToSlowPath_SkipsWhenFastPathSucceeded(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.retSvc = fakeRetrievalServiceWithFallback(t, true)

	es := &retrieval.EvidenceSet{PathType: retrieval.PathTypeFast, ActivationHits: []retrieval.ActivationHit{{LinkID: "l1"}}}
	g := &generated{result: &AnswerResult{HasAnswer: true, Citations: []string{"f1"}}}

	got := svc.maybeFallbackToSlowPath(context.Background(), retrieval.QueryContext{}, es, g, false)
	if got != g {
		t.Error("expected the original generated result to be returned unchanged when the fast-path answer already succeeded")
	}
}

func TestMaybeFallbackToSlowPath_SkipsWhenNotFastPath(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.retSvc = fakeRetrievalServiceWithFallback(t, true)

	es := &retrieval.EvidenceSet{PathType: retrieval.PathTypeFull}
	g := &generated{result: &AnswerResult{HasAnswer: false, Citations: []string{}}}

	got := svc.maybeFallbackToSlowPath(context.Background(), retrieval.QueryContext{}, es, g, false)
	if got != g {
		t.Error("expected no fallback attempt for a non-fast-path EvidenceSet")
	}
}

// fakeRetrievalServiceWithFallback builds a minimal retrieval.Service whose
// only observable behavior these tests need is FastPathFallbackEnabled();
// its store/indexes are never touched because the assertions above return
// before RetrieveSlowPathWithProgress would be called.
func fakeRetrievalServiceWithFallback(t *testing.T, enabled bool) *retrieval.Service {
	t.Helper()
	cfg := &config.Config{Retrieval: config.RetrievalConfig{FastPathFallback: enabled}}
	return retrieval.NewService(nil, nil, nil, nil, nil, cfg, nil, nil, nil)
}

func TestAnswerFromQuestion_FallbackDisabled_KeepsFastPathFailure(t *testing.T) {
	svc, db, question := setupFallbackTestService(t, false)

	result, err := svc.AnswerFromQuestion(context.Background(), question, false)
	if err != nil {
		t.Fatalf("AnswerFromQuestion: %v", err)
	}
	if result.EvidenceSet.PathType != retrieval.PathTypeFast {
		t.Errorf("path_type = %q, want fast (fallback disabled, should keep failed fast-path result)", result.EvidenceSet.PathType)
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected the (empty-citation) fast-path result to be returned as-is")
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM answers`).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 persisted answer, got %d", count)
	}
}
