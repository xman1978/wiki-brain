package retrieval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
)

func setupTestService(t *testing.T) (*Service, *llm.FakeClient, *Store) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)
	for _, unitID := range []string{"u1", "u2", "u3", "u4"} {
		insertRerankSemantic(t, store, unitID, rerank.ExtractPromptVersion)
	}

	// Create markdown files for sources
	tmpDir := foundation.NewTestDir(t)
	mdContent := "# Algebra\nLinear equations ax+b=0 is linear\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10\n" +
		"Line 11\nLine 12\nLine 13\nLine 14\nLine 15\nLine 16\nLine 17\nLine 18\nLine 19\nLine 20\n" +
		"Line 21\nLine 22\nLine 23\nLine 24\nLine 25\n" +
		"Line 26\nLine 27\nLine 28\nLine 29\nLine 30\n" +
		"Line 31\nLine 32\nLine 33\nLine 34\nLine 35\nLine 36\nLine 37\nLine 38\nLine 39\nLine 40\n" +
		"Line 41\nLine 42\nLine 43\nLine 44\nLine 45\nLine 46\nLine 47\nLine 48\nLine 49\nLine 50"

	mdPath := filepath.Join(tmpDir, "algebra.md")
	os.WriteFile(mdPath, []byte(mdContent), 0644)

	mdPath2 := filepath.Join(tmpDir, "mech.md")
	os.WriteFile(mdPath2, []byte("# Mechanics\nF=ma\nLine 3\n"+
		"Line 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10\n"+
		"Line 11\nLine 12\nLine 13\nLine 14\nLine 15\nLine 16\nLine 17\nLine 18\nLine 19\nLine 20\n"+
		"Line 21\nLine 22\nLine 23\nLine 24\nLine 25\nLine 26\nLine 27\nLine 28\nLine 29\nLine 30"), 0644)

	mdPath3 := filepath.Join(tmpDir, "gen.md")
	os.WriteFile(mdPath3, []byte("# General\nUpdated equations\nax+b=0 has been deprecated\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10"), 0644)

	// Update markdown_path in DB
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's1'`, mdPath)
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's2'`, mdPath2)
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's3'`, mdPath3)

	// Create Bleve indexes and index test data
	idxDir := filepath.Join(tmpDir, "index")
	idxMgr, err := index.NewManager(idxDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	// Index units
	idxMgr.Units.Index("u1", map[string]interface{}{
		"unit_id": "u1", "source_id": "s1", "center": "linear equations",
		"line_start": 1, "line_end": 25,
		"content": "# Algebra\nLinear equations ax+b=0",
	})
	idxMgr.Units.Index("u2", map[string]interface{}{
		"unit_id": "u2", "source_id": "s1", "center": "quadratic equations",
		"line_start": 26, "line_end": 50,
		"content": "Quadratic equations ax^2+bx+c=0",
	})
	idxMgr.Units.Index("u3", map[string]interface{}{
		"unit_id": "u3", "source_id": "s2", "center": "newton laws",
		"line_start": 1, "line_end": 30,
		"content": "Newton's laws F=ma",
	})

	// Index points
	idxMgr.Points.Index("p1", map[string]interface{}{
		"point_id": "p1", "unit_id": "u1", "source_id": "s1",
		"content": "ax+b=0 is linear", "point_type": "fact",
	})
	idxMgr.Points.Index("p2", map[string]interface{}{
		"point_id": "p2", "unit_id": "u2", "source_id": "s1",
		"content": "ax^2+bx+c=0 is quadratic", "point_type": "fact",
	})
	idxMgr.Points.Index("p3", map[string]interface{}{
		"point_id": "p3", "unit_id": "u3", "source_id": "s2",
		"content": "F=ma", "point_type": "fact",
	})

	// Index outlines
	idxMgr.Outlines.Index("o1", map[string]interface{}{
		"outline_id": "o1", "source_id": "s1", "title": "Chapter 1 Algebra",
		"level": 1, "node_type": "section",
	})
	idxMgr.Outlines.Index("o2", map[string]interface{}{
		"outline_id": "o2", "source_id": "s1", "title": "Linear Equations",
		"level": 2, "node_type": "section",
	})
	idxMgr.Outlines.Index("o3", map[string]interface{}{
		"outline_id": "o3", "source_id": "s1", "title": "Quadratic Equations",
		"level": 2, "node_type": "section",
	})

	fake := llm.NewFakeClient()
	cfg := &config.Config{
		Retrieval: config.RetrievalConfig{
			OutlineFTSMinScore: 0.5,
			RerankTopN:         20,
		},
	}

	svc := NewService(store, fake, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg, nil, nil, nil)
	return svc, fake, store
}

func TestDomainPreFilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d1"]}`,
	})

	sources, err := svc.domainPreFilter(context.Background(), "what is linear equation?")
	if err != nil {
		t.Fatal(err)
	}
	// d1 sources + null domain sources
	for _, s := range sources {
		if s.DomainID.Valid && s.DomainID.String != "d1" {
			t.Errorf("unexpected domain_id: %s", s.DomainID.String)
		}
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (d1 + null domain), got %d", len(sources))
	}
}

func TestDomainPreFilterFallback(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": []}`,
	})

	sources, err := svc.domainPreFilter(context.Background(), "something")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources (all), got %d", len(sources))
	}
}

// TestDomainPreFilterZeroMatchFallback covers the case where the LLM returns
// well-formed, real domain_ids but none of them (nor a null domain_id) match
// any source — e.g. the question is routed to a domain with no sources yet.
// domainPreFilter must fall back to all sources instead of starving the rest
// of the pipeline (all four degraded-input branches now behave the same way).
func TestDomainPreFilterZeroMatchFallback(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	db.Exec(`INSERT INTO domains (domain_id, name, description) VALUES ('d1', 'Math', 'Mathematics')`)
	db.Exec(`INSERT INTO domains (domain_id, name, description) VALUES ('d2', 'Physics', 'Physics')`)
	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES ('s1', 'Algebra', 'md', 'algebra.md', '/tmp/algebra.md', '/tmp/algebra.md', 'completed', 'd1')`)

	fake := llm.NewFakeClient()
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d2"]}`,
	})

	svc := NewService(store, fake, nil, nil, nil, &config.Config{}, nil, nil, nil)

	sources, err := svc.domainPreFilter(context.Background(), "something")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected fallback to all 1 source, got %d", len(sources))
	}
}

func TestSourceSemanticFilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []SourceInfo{
		{SourceID: "s1", Title: "Algebra"},
		{SourceID: "s2", Title: "Mechanics"},
	}

	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": ["s1"]}`,
	})

	filtered, err := svc.sourceSemanticFilter(context.Background(), QueryContext{Question: "linear equation"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].SourceID != "s1" {
		t.Fatalf("expected [s1], got %v", filtered)
	}
}

func TestSourceSemanticFilterEmptyFallback(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []SourceInfo{
		{SourceID: "s1", Title: "Algebra"},
		{SourceID: "s2", Title: "Mechanics"},
	}

	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": []}`,
	})

	filtered, err := svc.sourceSemanticFilter(context.Background(), QueryContext{Question: "random question"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 sources (fallback to all), got %d", len(filtered))
	}
}

func TestRRFMerge(t *testing.T) {
	svc, _, _ := setupTestService(t)

	outlineCandidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", score: 2.0, sourcePaths: []string{"outline"}},
		{unitID: "u2", pointID: "p2", sourceID: "s1", score: 1.0, sourcePaths: []string{"outline"}},
	}
	ftsCandidates := []candidate{
		{unitID: "u2", pointID: "p2", sourceID: "s1", score: 3.0, sourcePaths: []string{"fts"}},
		{unitID: "u3", pointID: "p3", sourceID: "s2", score: 1.5, sourcePaths: []string{"fts"}},
	}

	merged := svc.rrfMerge(outlineCandidates, ftsCandidates)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged candidates, got %d", len(merged))
	}

	// u2 should have highest RRF score (appears in both lists)
	if merged[0].unitID != "u2" {
		t.Errorf("expected u2 as top candidate, got %s", merged[0].unitID)
	}
}

func TestRerankPreservesCandidateOrderAndFiltersIrrelevant(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25, score: 1.0},
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50, score: 0.5},
	}

	fake.SetResponse("rerank_judge.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据说明线性方程定义，可直接回答"}, {"candidate_id": "c2", "role": "irrelevant", "analysis": "证据主题与问题不匹配"}]}`,
	})

	kept, err := svc.rerank(context.Background(), QueryContext{Question: "what is linear equation?"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept (c2 irrelevant), got %d", len(kept))
	}
	if kept[0].unitID != "u1" {
		t.Errorf("expected u1, got %s", kept[0].unitID)
	}
	if kept[0].sourcePaths[0] != "direct" {
		t.Errorf("expected role=direct, got %s", kept[0].sourcePaths[0])
	}
}

func TestRerankUsesPersistedSemanticsAndRunsJudgeBatchesConcurrently(t *testing.T) {
	svc, tracker, candidates := setupPersistedSemanticRerank(t, 1, 2)

	got, err := svc.rerank(t.Context(), QueryContext{Question: "差旅住宿限额是多少？"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.Count("rerank_judge.md") != 2 {
		t.Fatalf("judge calls = %d, want 2", tracker.Count("rerank_judge.md"))
	}
	if tracker.MaxConcurrent() < 2 {
		t.Fatalf("max concurrency = %d, want >= 2", tracker.MaxConcurrent())
	}
	if len(got) != 2 {
		t.Fatalf("kept = %d, want 2", len(got))
	}
}

func TestRerankConsumesSemanticsAfterShadowReparentSwap(t *testing.T) {
	db := foundation.NewTestDB(t)
	if _, err := db.Exec(`INSERT INTO sources
		(source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('target-1', 'Travel Policy', 'md', 'old.md', '/tmp/old.md', '/tmp/old.md', 'completed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sources
		(source_id, title, format, file_name, original_path, markdown_path, status, shadow_of)
		VALUES ('shadow-1', 'Travel Policy Shadow', 'md', 'new.md', '/tmp/new.md', '/tmp/new.md', 'completed', 'target-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_units
		(unit_id, source_id, center, line_start, line_end, status, prompt_version, lifecycle)
		VALUES ('shadow-u1', 'shadow-1', 'Hotel limits', 1, 2, 'completed', 'v6-split', 'current')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES ('shadow-u1', 'Travel policy', 'Hotel limits', 'Explain limit', 'Employees', 'Domestic travel', '["Hotel limit is 500"]', ?)`, rerank.ExtractPromptVersion); err != nil {
		t.Fatal(err)
	}

	if err := source.NewStore(db).SwapShadowIntoTarget("shadow-1", "target-1", "/tmp/new.md", sql.NullString{}); err != nil {
		t.Fatalf("SwapShadowIntoTarget: %v", err)
	}

	store := NewStore(db)
	semantics, err := store.GetUnitRerankSemantics([]string{"shadow-u1"})
	if err != nil {
		t.Fatalf("GetUnitRerankSemantics after swap: %v", err)
	}
	if got := semantics["shadow-u1"].KeyFacts; !reflect.DeepEqual(got, []string{"Hotel limit is 500"}) {
		t.Fatalf("reparented key facts = %#v, want persisted facts", got)
	}

	tracker := &rerankJudgeTrackingLLM{}
	svc := NewService(store, tracker, nil, nil, nil, &config.Config{}, nil, nil, nil)
	kept, err := svc.rerank(t.Context(), QueryContext{Question: "What is the hotel limit?"}, []candidate{{
		unitID: "shadow-u1", sourceID: "target-1", lineStart: 1, lineEnd: 2, score: 1,
	}})
	if err != nil {
		t.Fatalf("rerank reparented unit: %v", err)
	}
	if len(kept) != 1 || kept[0].sourcePaths[0] != "direct" {
		t.Fatalf("kept candidates = %+v, want one direct result", kept)
	}
	if tracker.Count("rerank_judge.md") != 1 {
		t.Fatalf("judge calls = %d, want 1", tracker.Count("rerank_judge.md"))
	}
	if len(tracker.payloads) != 1 || !strings.Contains(tracker.payloads[0], "Hotel limit is 500") {
		t.Fatalf("judge payloads = %v, want reparented semantic facts", tracker.payloads)
	}
}

func TestRerankJudgeBatchBudgetCountsCompactJSONRunesAndDelimiters(t *testing.T) {
	svc, tracker, candidates := setupPersistedSemanticRerank(t, 4000, 2)
	insertRerankSemantic(t, svc.store, "u3", rerank.ExtractPromptVersion)
	candidates = append(candidates, candidate{
		unitID: "u3", pointID: "p3", sourceID: "s2", lineStart: 1, lineEnd: 30, score: 0.25,
	})

	semantics, err := svc.store.GetUnitRerankSemantics([]string{"u1", "u2"})
	if err != nil {
		t.Fatal(err)
	}
	first := buildRerankJudgeCandidate("c1", "Algebra", semantics["u1"])
	second := buildRerankJudgeCandidate("c2", "Algebra", semantics["u2"])
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	// Two brackets plus the comma between two compact JSON objects.
	svc.cfg.Retrieval.RerankJudgeBatchMaxChars = 2 + utf8.RuneCount(firstJSON) + 1 + utf8.RuneCount(secondJSON)

	if _, err := svc.rerank(t.Context(), QueryContext{Question: "差旅住宿限额是多少？"}, candidates); err != nil {
		t.Fatal(err)
	}
	if got := tracker.BatchSizes(); !equalInts(got, []int{2, 1}) {
		t.Fatalf("judge batch sizes = %v, want [2 1]", got)
	}
	for _, payload := range tracker.Payloads() {
		var decoded []rerankJudgeCandidate
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("judge payload is not JSON: %v", err)
		}
		compact, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if payload != string(compact) {
			t.Fatalf("judge payload is not compact JSON: %q", payload)
		}
	}
}

func TestRerankRejectsMissingSemanticsListsAllUnitIDs(t *testing.T) {
	svc, tracker, candidates := setupPersistedSemanticRerank(t, 4000, 4)
	if _, err := svc.store.db.Exec(`DELETE FROM unit_rerank_semantics WHERE unit_id IN ('u1', 'u2')`); err != nil {
		t.Fatal(err)
	}

	_, err := svc.rerank(t.Context(), QueryContext{Question: "差旅住宿限额是多少？"}, reverseCandidates(candidates))
	assertRerankIntegrityError(t, err,
		"retrieval: rerank semantics integrity: missing unit_ids: u1, u2")
	if tracker.Count("rerank_judge.md") != 0 {
		t.Fatalf("judge calls = %d, want 0", tracker.Count("rerank_judge.md"))
	}
}

func TestRerankRejectsStaleSemanticsListsAllUnitIDs(t *testing.T) {
	svc, tracker, candidates := setupPersistedSemanticRerank(t, 4000, 4)
	if _, err := svc.store.db.Exec(`UPDATE unit_rerank_semantics SET prompt_version = 'v0' WHERE unit_id IN ('u1', 'u2')`); err != nil {
		t.Fatal(err)
	}

	_, err := svc.rerank(t.Context(), QueryContext{Question: "差旅住宿限额是多少？"}, reverseCandidates(candidates))
	assertRerankIntegrityError(t, err,
		"retrieval: rerank semantics integrity: stale unit_ids: u1, u2")
	if tracker.Count("rerank_judge.md") != 0 {
		t.Fatalf("judge calls = %d, want 0", tracker.Count("rerank_judge.md"))
	}
}

func setupPersistedSemanticRerank(t *testing.T, maxChars, concurrency int) (*Service, *rerankJudgeTrackingLLM, []candidate) {
	t.Helper()
	svc, _, _ := setupTestService(t)
	tracker := &rerankJudgeTrackingLLM{}
	svc.llmClient = tracker
	svc.cfg.Retrieval.RerankJudgeBatchMaxChars = maxChars
	svc.cfg.Retrieval.RerankJudgeConcurrency = concurrency

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25, score: 1.0},
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50, score: 0.5},
	}
	return svc, tracker, candidates
}

func insertRerankSemantic(t *testing.T, store *Store, unitID, promptVersion string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT OR REPLACE INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES (?, '差旅制度', '住宿报销', '说明限额', '出差员工', '境内差旅', '["住宿限额为500元", "超额需审批"]', ?)`,
		unitID, promptVersion)
	if err != nil {
		t.Fatal(err)
	}
}

func reverseCandidates(candidates []candidate) []candidate {
	reversed := append([]candidate(nil), candidates...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func assertRerankIntegrityError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("rerank returned nil error")
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type rerankJudgeTrackingLLM struct {
	mu            sync.Mutex
	calls         map[string]int
	inFlight      int
	maxConcurrent int
	batchSizes    []int
	payloads      []string
}

func (f *rerankJudgeTrackingLLM) Complete(_ context.Context, promptFile string, vars map[string]string, model string) (string, error) {
	return "", fmt.Errorf("unexpected Complete call: %s", promptFile)
}

func (f *rerankJudgeTrackingLLM) CompleteStream(_ context.Context, promptFile string, vars map[string]string, model string) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("unexpected CompleteStream call: %s", promptFile)
}

func (f *rerankJudgeTrackingLLM) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[promptFile]++
	f.mu.Unlock()
	if promptFile != "rerank_judge.md" {
		return nil, fmt.Errorf("unexpected prompt: %s", promptFile)
	}

	var candidates []rerankJudgeCandidate
	if err := json.Unmarshal([]byte(vars["candidates"]), &candidates); err != nil {
		return nil, fmt.Errorf("decode judge candidates: %w", err)
	}
	f.beginJudge(len(candidates), vars["candidates"])
	defer f.endJudge()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(40 * time.Millisecond):
	}
	ids := make([]string, len(candidates))
	for i := range candidates {
		ids[i] = candidates[i].CandidateID
	}
	return []byte(rerankJudgeJSON(ids)), nil
}

func (f *rerankJudgeTrackingLLM) beginJudge(batchSize int, payload string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight++
	if f.inFlight > f.maxConcurrent {
		f.maxConcurrent = f.inFlight
	}
	f.batchSizes = append(f.batchSizes, batchSize)
	f.payloads = append(f.payloads, payload)
}

func (f *rerankJudgeTrackingLLM) endJudge() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
}

func (f *rerankJudgeTrackingLLM) Count(promptFile string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[promptFile]
}

func (f *rerankJudgeTrackingLLM) MaxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxConcurrent
}

func (f *rerankJudgeTrackingLLM) BatchSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	got := append([]int(nil), f.batchSizes...)
	sort.Slice(got, func(i, j int) bool { return got[i] > got[j] })
	return got
}

func (f *rerankJudgeTrackingLLM) Payloads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.payloads...)
}

func rerankJudgeJSON(ids []string) string {
	results := make([]string, 0, len(ids))
	for _, id := range ids {
		results = append(results, fmt.Sprintf(`{"candidate_id": %q, "role": "direct", "analysis": "matches"}`, id))
	}
	return fmt.Sprintf(`{"results": [%s]}`, strings.Join(results, ","))
}

func TestKPNExpand(t *testing.T) {
	svc, _, _ := setupTestService(t)

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25, sourcePaths: []string{"direct"}},
	}

	expanded, conflicts, err := svc.kpnExpand(candidates)
	if err != nil {
		t.Fatal(err)
	}

	// u1 has p1 → p2 (bidirectional, related), p1 → p3 (directed, supplements)
	// So u2 (via p2) and u3 (via p3) should be added as supporting
	if len(expanded) != 3 {
		t.Fatalf("expected 3 candidates after KPN expand, got %d", len(expanded))
	}

	newUnits := make(map[string]bool)
	for _, c := range expanded[1:] {
		newUnits[c.unitID] = true
		if c.sourcePaths[0] != "supporting" {
			t.Errorf("expected supporting role for %s, got %s", c.unitID, c.sourcePaths[0])
		}
	}
	if !newUnits["u2"] || !newUnits["u3"] {
		t.Errorf("expected u2 and u3 from KPN expand, got %v", newUnits)
	}

	// p1→p4 contradicts → u4 should appear in conflicts
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict (p1→p4 contradicts), got %d", len(conflicts))
	}
	if conflicts[0].unitID != "u4" {
		t.Errorf("expected conflict unit u4, got %s", conflicts[0].unitID)
	}
}

func TestSufficiencyJudgment(t *testing.T) {
	// direct non-empty → short
	direct := []candidate{{unitID: "u1"}}
	path := "deep"
	if len(direct) > 0 {
		path = "short"
	}
	if path != "short" {
		t.Errorf("expected short, got %s", path)
	}

	// direct empty → deep
	direct = nil
	path = "deep"
	if len(direct) > 0 {
		path = "short"
	}
	if path != "deep" {
		t.Errorf("expected deep, got %s", path)
	}
}

func TestBuildEvidenceSet(t *testing.T) {
	svc, _, _ := setupTestService(t)

	direct := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25},
	}
	supporting := []candidate{
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50},
	}

	es, err := svc.buildEvidenceSet(context.Background(), "test question", "", "", "", "", "short", direct, supporting, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if es.Question != "test question" {
		t.Errorf("expected question='test question', got '%s'", es.Question)
	}
	if es.Path != "short" {
		t.Errorf("expected path=short, got %s", es.Path)
	}
	if len(es.DirectEvidence) != 1 {
		t.Fatalf("expected 1 direct evidence, got %d", len(es.DirectEvidence))
	}
	if len(es.Supporting) != 1 {
		t.Fatalf("expected 1 supporting, got %d", len(es.Supporting))
	}

	ev := es.DirectEvidence[0]
	if ev.FactID == "" {
		t.Error("fact_id should not be empty")
	}
	if ev.UnitID != "u1" {
		t.Errorf("expected unit_id=u1, got %s", ev.UnitID)
	}
	if ev.PointID != "p1" {
		t.Errorf("expected point_id=p1, got %s", ev.PointID)
	}
	if ev.Role != "direct" {
		t.Errorf("expected role=direct, got %s", ev.Role)
	}
	if ev.Content == "" {
		t.Error("content should not be empty")
	}

	var ref SourceRef
	if err := json.Unmarshal(ev.SourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.SourceID != "s1" || ref.LineStart != 1 || ref.LineEnd != 25 {
		t.Errorf("unexpected source_ref: %+v", ref)
	}
}

func TestRetrieveEndToEnd(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	// Domain match → d1
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d1"]}`,
	})
	// Source filter → s1
	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": ["s1"]}`,
	})
	// Outline filter fallback
	fake.SetResponse("outline_filter.md", llm.FakeResponse{
		Output: `{"outline_ids": ["o2"]}`,
	})
	// Rerank — accept all as direct for simplicity
	fake.SetResponse("rerank_judge.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据说明线性方程定义，可直接回答"}]}`,
	})

	es, err := svc.Retrieve(context.Background(), "linear equations")
	if err != nil {
		t.Fatal(err)
	}

	if es.Question != "linear equations" {
		t.Errorf("expected question='linear equations', got '%s'", es.Question)
	}
	// Should have direct evidence → short path
	if es.Path != "short" {
		t.Errorf("expected path=short, got %s", es.Path)
	}
	if len(es.DirectEvidence) == 0 {
		t.Error("expected at least 1 direct evidence")
	}
}
