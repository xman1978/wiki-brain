package trace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

type testQuestion struct {
	ID                   string   `json:"id"`
	Question             string   `json:"question"`
	Category             string   `json:"category"`
	ExpectedPath         string   `json:"expected_path"`
	ExpectedSources      []string `json:"expected_sources"`
	ExpectedAnswerPoints []string `json:"expected_answer_points"`
}

func TestIntegrationTraceQuality(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run real LLM integration test")
	}

	projectRoot := findProjectRoot(t)
	testdataDir := filepath.Join(projectRoot, "test", "testdata")

	cfg, err := config.Load(filepath.Join(projectRoot, "config", "config.yml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	promptsDir := filepath.Join(projectRoot, "config", "prompts")
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, promptsDir)
	if err != nil {
		t.Fatalf("create llm client: %v", err)
	}

	tmpDir := t.TempDir()
	seedDst := filepath.Join(tmpDir, "seed.db")
	copyFile(t, filepath.Join(testdataDir, "seed.db"), seedDst)

	database, err := fdb.Open(seedDst)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	idxDst := filepath.Join(tmpDir, "searchindex_fresh")
	idxMgr, err := index.NewManager(idxDst)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idxMgr.Close()
	rebuildIndexes(t, database, idxMgr, testdataDir)

	retStore := retrieval.NewStore(database)
	retSvc := retrieval.NewService(retStore, llmClient, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg)

	ansStore := answer.NewStore(database)
	traceStore := NewStore(database)
	traceSvc := NewService(traceStore)

	// Wire up queue so trace is processed synchronously with a wait signal
	var traceWg sync.WaitGroup
	q := queue.New(100)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {
		defer traceWg.Done()
		tt, ok := payload.(*queue.TraceTask)
		if !ok {
			return
		}
		ar, ok := tt.Result.(*answer.AnswerResult)
		if !ok {
			return
		}
		traceSvc.ProcessTrace(ar)
	})
	q.Start()
	defer q.Shutdown()

	ansSvc := answer.NewService(ansStore, llmClient, q, retSvc)

	qData, err := os.ReadFile(filepath.Join(testdataDir, "retrieval_questions.json"))
	if err != nil {
		t.Fatalf("read questions: %v", err)
	}
	var questions []testQuestion
	if err := json.Unmarshal(qData, &questions); err != nil {
		t.Fatalf("parse questions: %v", err)
	}

	reportPath := filepath.Join(projectRoot, "test", "testdata", "trace_report.txt")
	reportFile, err := os.Create(reportPath)
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	defer reportFile.Close()

	slog.SetDefault(slog.New(slog.NewTextHandler(reportFile, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logf := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		fmt.Fprintln(reportFile, line)
	}

	divider := strings.Repeat("=", 120)
	logf("Trace Quality Report")
	logf("Generated: %s", time.Now().Format("2006-01-02 15:04:05"))
	logf("%s", divider)

	type traceResult struct {
		questionID string
		answerID   string
		trace      *Trace
		events     []LearningEvent
		coocs      []Cooccurrence
		elapsed    time.Duration
	}
	var results []traceResult

	for _, tq := range questions {
		logf("")
		logf("[%s] %s", tq.ID, tq.Question)
		logf("  Category: %s | Expected path: %s", tq.Category, tq.ExpectedPath)
		logf("%s", strings.Repeat("-", 100))

		traceWg.Add(1)
		start := time.Now()
		ar, err := ansSvc.AnswerFromQuestion(context.Background(), tq.Question, false)
		if err != nil {
			traceWg.Done()
			logf("  ERROR: %v", err)
			results = append(results, traceResult{questionID: tq.ID, elapsed: time.Since(start)})
			continue
		}

		// Wait for async trace processing to complete
		traceWg.Wait()
		elapsed := time.Since(start)

		// Fetch trace record by answer_id
		traces, err := traceStore.ListTraces("", ar.AnswerID, 10, 0)
		if err != nil || len(traces) == 0 {
			logf("  ⚠ No trace found for answer_id=%s", ar.AnswerID)
			results = append(results, traceResult{questionID: tq.ID, answerID: ar.AnswerID, elapsed: elapsed})
			continue
		}
		tr, _ := traceStore.GetTrace(traces[0].TraceID)

		// Fetch learning events for this trace
		allEvents, _ := traceStore.ListLearningEvents("", -1, 100)
		var traceEvents []LearningEvent
		for _, e := range allEvents {
			if e.TraceID == tr.TraceID {
				traceEvents = append(traceEvents, e)
			}
		}

		// Fetch cooccurrence entries related to this trace's point_ids
		var traceCoocs []Cooccurrence
		pointIDs := tr.DirectPointIDs
		if tr.RetrievalQuality == QualityPartial {
			pointIDs = supportingCitedPointIDs(ar.EvidenceSet, ar.Citations)
		}
		for _, pid := range pointIDs {
			coocs, _ := traceStore.ListCooccurrence(pid, 0, 50)
			traceCoocs = append(traceCoocs, coocs...)
		}

		results = append(results, traceResult{
			questionID: tq.ID,
			answerID:   ar.AnswerID,
			trace:      tr,
			events:     traceEvents,
			coocs:      traceCoocs,
			elapsed:    elapsed,
		})

		// Detailed per-question report
		logf("  Answer:   id=%s path=%s has_answer=%v citations=%d",
			ar.AnswerID, ar.Path, ar.HasAnswer, len(ar.Citations))
		logf("  Trace:    id=%s quality=%s path=%s",
			tr.TraceID, tr.RetrievalQuality, tr.Path)
		logf("  Question: hash=%s terms=%q",
			tr.QuestionHash, tr.QuestionTerms)
		logf("  DirectPointIDs: %v", tr.DirectPointIDs)
		logf("  LearningEvents: %d", len(traceEvents))
		for _, e := range traceEvents {
			logf("    - type=%s payload=%s", e.EventType, e.Payload)
		}
		logf("  Cooccurrence entries: %d", len(traceCoocs))
		for _, c := range traceCoocs {
			logf("    - terms=%q point_id=%s hit=%d confident=%d",
				c.QuestionTerms, c.PointID, c.HitCount, c.ConfidentCount)
		}
		logf("  Time: %.1fs", elapsed.Seconds())
	}

	// --- Duplicate question dedup test ---
	logf("")
	logf("%s", divider)
	logf("DEDUP TEST: Repeat q01")
	logf("%s", divider)

	traceWg.Add(1)
	ar2, err := ansSvc.AnswerFromQuestion(context.Background(), questions[0].Question, false)
	if err != nil {
		traceWg.Done()
		logf("  ERROR on repeat: %v", err)
	} else {
		traceWg.Wait()
		traces2, _ := traceStore.ListTraces("", ar2.AnswerID, 10, 0)
		if len(traces2) > 0 {
			tr2, _ := traceStore.GetTrace(traces2[0].TraceID)
			logf("  Repeat trace: id=%s quality=%s hash=%s",
				tr2.TraceID, tr2.RetrievalQuality, tr2.QuestionHash)

			// Check cooccurrence was not double-counted
			if results[0].trace != nil {
				origHash := results[0].trace.QuestionHash
				repeatHash := tr2.QuestionHash
				logf("  Original hash: %s | Repeat hash: %s | Same: %v",
					origHash, repeatHash, origHash == repeatHash)
			}

			for _, pid := range tr2.DirectPointIDs {
				coocs, _ := traceStore.ListCooccurrence(pid, 0, 50)
				for _, c := range coocs {
					logf("  Cooc after repeat: terms=%q point=%s hit=%d confident=%d",
						c.QuestionTerms, c.PointID, c.HitCount, c.ConfidentCount)
					if c.HitCount > 1 {
						logf("  ⚠ DEDUP FAILED: hit_count > 1 for repeated question")
					}
				}
			}
		}
	}

	// --- Summary ---
	logf("")
	logf("%s", divider)
	logf("SUMMARY")
	logf("%s", divider)

	qualityCounts := map[string]int{}
	pathMatch := 0
	totalEvents := 0
	totalCoocs := 0
	hasTraceCount := 0

	for i, r := range results {
		tq := questions[i]
		if r.trace != nil {
			hasTraceCount++
			qualityCounts[r.trace.RetrievalQuality]++
			totalEvents += len(r.events)
			totalCoocs += len(r.coocs)

			// Verify quality grade matches expectations
			if tq.Category == "irrelevant" {
				if r.trace.RetrievalQuality == QualityGap {
					pathMatch++
				}
			} else {
				if r.trace.RetrievalQuality == QualityConfident || r.trace.RetrievalQuality == QualityPartial {
					pathMatch++
				}
			}
		}
	}

	logf("Questions: %d | Traces created: %d/%d", len(questions), hasTraceCount, len(questions))
	logf("Quality distribution: confident=%d partial=%d gap=%d",
		qualityCounts[QualityConfident], qualityCounts[QualityPartial], qualityCounts[QualityGap])
	logf("Quality match (relevant→confident/partial, irrelevant→gap): %d/%d", pathMatch, len(questions))
	logf("Total learning events: %d | Total cooccurrence entries: %d", totalEvents, totalCoocs)

	logf("")
	logf("%-5s %-10s %-10s %-6s %-8s %-6s %-6s %s",
		"ID", "Category", "Quality", "Path", "DirPIDs", "Events", "Coocs", "Time")
	for i, r := range results {
		tq := questions[i]
		quality, path, dirPIDs, events, coocs := "-", "-", "-", "-", "-"
		elapsed := "-"
		if r.trace != nil {
			quality = r.trace.RetrievalQuality
			path = r.trace.Path
			dirPIDs = fmt.Sprintf("%d", len(r.trace.DirectPointIDs))
			events = fmt.Sprintf("%d", len(r.events))
			coocs = fmt.Sprintf("%d", len(r.coocs))
		}
		if r.elapsed > 0 {
			elapsed = r.elapsed.Round(time.Millisecond).String()
		}
		logf("%-5s %-10s %-10s %-6s %-8s %-6s %-6s %s",
			tq.ID, tq.Category, quality, path, dirPIDs, events, coocs, elapsed)
	}

	// Assertions
	if hasTraceCount != len(questions) {
		t.Errorf("expected trace for all %d questions, got %d", len(questions), hasTraceCount)
	}

	// irrelevant questions should produce gap + knowledge_gap event
	for i, r := range results {
		tq := questions[i]
		if tq.Category == "irrelevant" && r.trace != nil {
			if r.trace.RetrievalQuality != QualityGap {
				t.Errorf("[%s] irrelevant question: expected gap, got %s", tq.ID, r.trace.RetrievalQuality)
			}
			hasGapEvent := false
			for _, e := range r.events {
				if e.EventType == "knowledge_gap" {
					hasGapEvent = true
				}
			}
			if !hasGapEvent {
				t.Errorf("[%s] irrelevant question: expected knowledge_gap event", tq.ID)
			}
		}
	}

	// confident traces should have non-empty direct_point_ids
	for i, r := range results {
		tq := questions[i]
		if r.trace != nil && r.trace.RetrievalQuality == QualityConfident {
			if len(r.trace.DirectPointIDs) == 0 {
				t.Errorf("[%s] confident trace has empty direct_point_ids", tq.ID)
			}
		}
	}

	t.Logf("Report written to %s", reportPath)
}

// --- Test helpers (duplicated from answer/integration_test.go to avoid cross-package dep) ---

func rebuildIndexes(t *testing.T, db *sql.DB, idxMgr *index.Manager, testdataDir string) {
	t.Helper()

	rows, err := db.Query(`SELECT outline_id, source_id, title, COALESCE(summary,''), level, node_type FROM source_outlines`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, sourceID, title, summary, nodeType string
		var level int
		rows.Scan(&id, &sourceID, &title, &summary, &level, &nodeType)
		idxMgr.Outlines.Index(id, map[string]interface{}{
			"outline_id": id, "source_id": sourceID,
			"title": title, "summary": summary,
			"level": level, "node_type": nodeType,
		})
	}

	rows2, err := db.Query(`SELECT unit_id, source_id, center, line_start, line_end FROM knowledge_units WHERE status='completed'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id, sourceID, center string
		var lineStart, lineEnd int
		rows2.Scan(&id, &sourceID, &center, &lineStart, &lineEnd)
		content := readContent(filepath.Join(testdataDir, "markdown", sourceID+".md"), lineStart, lineEnd)
		idxMgr.Units.Index(id, map[string]interface{}{
			"unit_id": id, "source_id": sourceID, "center": center,
			"line_start": lineStart, "line_end": lineEnd, "content": content,
		})
	}

	rows3, err := db.Query(`SELECT point_id, unit_id, source_id, content, point_type FROM knowledge_points`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var pid, uid, sid, content, ptype string
		rows3.Scan(&pid, &uid, &sid, &content, &ptype)
		idxMgr.Points.Index(pid, map[string]interface{}{
			"point_id": pid, "unit_id": uid, "source_id": sid,
			"content": content, "point_type": ptype,
		})
	}
}

func readContent(path string, lineStart, lineEnd int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find project root")
		}
		dir = parent
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	defer out.Close()
	io.Copy(out, in)
}
