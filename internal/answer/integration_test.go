package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"database/sql"

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

func TestIntegrationAnswerQuality(t *testing.T) {
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

	ansStore := NewStore(database)
	q := queue.New(100)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	defer q.Shutdown()

	ansSvc := NewService(ansStore, llmClient, q, retSvc)

	qData, err := os.ReadFile(filepath.Join(testdataDir, "retrieval_questions.json"))
	if err != nil {
		t.Fatalf("read questions: %v", err)
	}
	var questions []testQuestion
	if err := json.Unmarshal(qData, &questions); err != nil {
		t.Fatalf("parse questions: %v", err)
	}

	reportPath := filepath.Join(projectRoot, "test", "testdata", "answer_report.txt")
	reportFile, err := os.Create(reportPath)
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	defer reportFile.Close()

	slog.SetDefault(slog.New(slog.NewTextHandler(reportFile, &slog.HandlerOptions{Level: slog.LevelInfo})))

	logf := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		fmt.Fprintln(reportFile, line)
	}

	divider := strings.Repeat("=", 120)
	logf("Answer Quality Report")
	logf("Generated: %s", time.Now().Format("2006-01-02 15:04:05"))
	logf("%s", divider)

	var totalDuration time.Duration
	type qResult struct {
		id          string
		path        string
		hitCount    int
		totalPoints int
		hasAnswer   bool
		elapsed     time.Duration
	}
	var results []qResult

	for _, tq := range questions {
		logf("")
		logf("[%s] %s", tq.ID, tq.Question)
		logf("  Category: %s | Expected path: %s", tq.Category, tq.ExpectedPath)
		logf("%s", strings.Repeat("-", 100))

		start := time.Now()
		ar, err := ansSvc.AnswerFromQuestion(context.Background(), tq.Question, false)
		elapsed := time.Since(start)
		totalDuration += elapsed

		if err != nil {
			logf("  ERROR: %v (%.1fs)", err, elapsed.Seconds())
			results = append(results, qResult{id: tq.ID, path: "ERROR", elapsed: elapsed})
			continue
		}

		logf("  Actual path: %s | has_answer: %v | citations: %d | time: %.1fs",
			ar.Path, ar.HasAnswer, len(ar.Citations), elapsed.Seconds())
		logf("")

		logf("  --- Generated Answer ---")
		for _, line := range strings.Split(ar.Content, "\n") {
			logf("  %s", line)
		}
		logf("")

		if len(tq.ExpectedAnswerPoints) > 0 {
			logf("  --- Expected Answer Points ---")
			hitCount := 0
			contentLower := strings.ToLower(ar.Content)
			for i, point := range tq.ExpectedAnswerPoints {
				hit := strings.Contains(contentLower, strings.ToLower(point))
				marker := "✗"
				if hit {
					marker = "✓"
					hitCount++
				}
				logf("  %s [%d] %s", marker, i+1, point)
			}
			logf("")
			logf("  Coverage: %d/%d (%.0f%%)", hitCount, len(tq.ExpectedAnswerPoints),
				float64(hitCount)/float64(len(tq.ExpectedAnswerPoints))*100)

			results = append(results, qResult{
				id: tq.ID, path: ar.Path, hitCount: hitCount,
				totalPoints: len(tq.ExpectedAnswerPoints), hasAnswer: ar.HasAnswer,
				elapsed: elapsed,
			})
		} else {
			if tq.Category == "irrelevant" {
				if ar.HasAnswer && ar.Path != "none" {
					logf("  ⚠ Irrelevant question got an answer (expected none)")
				} else {
					logf("  ✓ Correctly identified as no-evidence / irrelevant")
				}
			}
			results = append(results, qResult{
				id: tq.ID, path: ar.Path, hasAnswer: ar.HasAnswer, elapsed: elapsed,
			})
		}
	}

	logf("")
	logf("%s", divider)
	logf("SUMMARY")
	logf("%s", divider)

	totalHit, totalPoints := 0, 0
	pathMatches := 0
	for i, r := range results {
		tq := questions[i]
		if r.path == tq.ExpectedPath || (tq.Category == "irrelevant" && (r.path == "none" || r.path == "deep")) {
			pathMatches++
		}
		totalHit += r.hitCount
		totalPoints += r.totalPoints
	}

	logf("Questions: %d | Path match: %d/%d", len(questions), pathMatches, len(questions))
	if totalPoints > 0 {
		logf("Answer point coverage: %d/%d (%.0f%%)", totalHit, totalPoints, float64(totalHit)/float64(totalPoints)*100)
	}
	logf("Total time: %.1fs | Avg: %.1fs", totalDuration.Seconds(), totalDuration.Seconds()/float64(len(questions)))

	logf("")
	logf("%-5s %-10s %-6s %-6s %-8s %-10s %s",
		"ID", "Category", "ExPath", "Path", "Cover", "Time", "Question")
	for i, r := range results {
		tq := questions[i]
		cover := "-"
		if r.totalPoints > 0 {
			cover = fmt.Sprintf("%d/%d", r.hitCount, r.totalPoints)
		}
		logf("%-5s %-10s %-6s %-6s %-8s %-10s %s",
			r.id, tq.Category, tq.ExpectedPath, r.path, cover,
			r.elapsed.Round(time.Millisecond), truncate(tq.Question, 40))
	}

	t.Logf("Report written to %s", reportPath)
}

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

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
