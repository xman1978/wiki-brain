package retrieval

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
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type testQuestion struct {
	ID              string   `json:"id"`
	Question        string   `json:"question"`
	Category        string   `json:"category"`
	ExpectedPath    string   `json:"expected_path"`
	ExpectedSources []string `json:"expected_sources"`
}

func TestIntegrationRetrieval(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run real LLM integration test")
	}

	projectRoot := findProjectRoot(t)
	testdataDir := filepath.Join(projectRoot, "test", "testdata")

	// Load real config
	cfgPath := filepath.Join(projectRoot, "config", "config.yml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Create real LLM client
	promptsDir := filepath.Join(projectRoot, "config", "prompts")
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, promptsDir)
	if err != nil {
		t.Fatalf("create llm client: %v", err)
	}

	// Copy seed.db to temp (SQLite WAL)
	tmpDir := t.TempDir()
	seedDst := filepath.Join(tmpDir, "seed.db")
	copyFile(t, filepath.Join(testdataDir, "seed.db"), seedDst)

	database, err := db.Open(seedDst)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Create fresh indexes and rebuild from DB (NewManager auto-inits gse)
	idxDst := filepath.Join(tmpDir, "searchindex_fresh")
	idxMgr, err := index.NewManager(idxDst)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idxMgr.Close()

	rebuildIndexes(t, database, idxMgr, testdataDir)

	store := NewStore(database)
	svc := NewService(store, llmClient, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg)

	// Load questions
	qData, err := os.ReadFile(filepath.Join(testdataDir, "retrieval_questions.json"))
	if err != nil {
		t.Fatalf("read questions: %v", err)
	}
	var questions []testQuestion
	if err := json.Unmarshal(qData, &questions); err != nil {
		t.Fatalf("parse questions: %v", err)
	}

	// Write results to file for visibility (RTK filters test output)
	reportPath := filepath.Join(projectRoot, "test", "testdata", "retrieval_report.txt")
	reportFile, err := os.Create(reportPath)
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	defer reportFile.Close()

	// Redirect slog to report file
	slog.SetDefault(slog.New(slog.NewTextHandler(reportFile, &slog.HandlerOptions{Level: slog.LevelInfo})))

	logf := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		fmt.Fprintln(reportFile, line)
	}

	// Print header
	header := fmt.Sprintf("%-5s %-10s %-6s %-6s %-4s %-4s %-10s  %s",
		"ID", "Category", "Expect", "Actual", "D#", "S#", "Time", "Question")
	divider := strings.Repeat("-", 130)
	logf("%s", header)
	logf("%s", divider)

	type result struct {
		id       string
		question string
		category string
		expected string
		actual   string
		direct   int
		support  int
		elapsed  time.Duration
		err      error
		sources  map[string]bool
	}
	var results []result
	var totalDuration time.Duration

	for _, q := range questions {
		start := time.Now()
		es, err := svc.Retrieve(context.Background(), q.Question)
		elapsed := time.Since(start)
		totalDuration += elapsed

		r := result{
			id:       q.id(),
			question: q.Question,
			category: q.Category,
			expected: q.ExpectedPath,
			elapsed:  elapsed,
			err:      err,
			sources:  make(map[string]bool),
		}

		if err != nil {
			r.actual = "ERROR"
			logf("%-5s %-10s %-6s %-6s %-4s %-4s %-10s  %s",
				q.ID, q.Category, q.ExpectedPath, "ERROR", "-", "-",
				elapsed.Round(time.Millisecond), truncateQ(q.Question, 45))
			logf("        ERROR: %v", err)
			results = append(results, r)
			continue
		}

		r.actual = es.Path
		r.direct = len(es.DirectEvidence)
		r.support = len(es.Supporting)

		pathFlag := ""
		if es.Path != q.ExpectedPath {
			pathFlag = " <MISMATCH>"
		}

		logf("%-5s %-10s %-6s %-6s %-4d %-4d %-10s  %s%s",
			q.ID, q.Category, q.ExpectedPath, es.Path,
			r.direct, r.support,
			elapsed.Round(time.Millisecond),
			truncateQ(q.Question, 45), pathFlag)

		for _, ev := range es.DirectEvidence {
			var ref SourceRef
			json.Unmarshal(ev.SourceRef, &ref)
			r.sources[ref.SourceID] = true
			logf("        [direct]     src=%-10s unit=%s L%-3d-%-3d  %s",
				ref.SourceID, ev.UnitID[:8], ref.LineStart, ref.LineEnd,
				truncateQ(strings.ReplaceAll(ev.Content, "\n", " "), 60))
		}
		for _, ev := range es.Supporting {
			var ref SourceRef
			json.Unmarshal(ev.SourceRef, &ref)
			r.sources[ref.SourceID] = true
			logf("        [supporting] src=%-10s unit=%s L%-3d-%-3d  %s",
				ref.SourceID, ev.UnitID[:8], ref.LineStart, ref.LineEnd,
				truncateQ(strings.ReplaceAll(ev.Content, "\n", " "), 60))
		}

		results = append(results, r)
	}

	// Summary
	logf("%s", divider)
	logf("Total: %s  Avg: %s",
		totalDuration.Round(time.Millisecond),
		(totalDuration / time.Duration(len(questions))).Round(time.Millisecond))
	logf("")

	// Issue tracking
	var issues []string
	for i, r := range results {
		q := questions[i]

		if r.err != nil {
			issues = append(issues, fmt.Sprintf("[%s] LLM error: %v", q.ID, r.err))
			continue
		}

		if r.actual != r.expected {
			issues = append(issues, fmt.Sprintf("[%s] path: expected=%s got=%s", q.ID, r.expected, r.actual))
		}

		for _, expSrc := range q.ExpectedSources {
			if !r.sources[expSrc] {
				issues = append(issues, fmt.Sprintf("[%s] missing source %s in evidence", q.ID, expSrc))
			}
		}

		if q.Category == "irrelevant" && r.direct > 0 {
			issues = append(issues, fmt.Sprintf("[%s] irrelevant question should have 0 direct evidence, got %d", q.ID, r.direct))
		}
	}

	if len(issues) > 0 {
		logf("=== ISSUES (%d) ===", len(issues))
		for _, iss := range issues {
			logf("  %s", iss)
		}
	} else {
		logf("=== ALL CHECKS PASSED ===")
	}

	t.Logf("Report written to %s", reportPath)
}

func rebuildIndexes(t *testing.T, db *sql.DB, idxMgr *index.Manager, testdataDir string) {
	t.Helper()

	// Index outlines
	rows, err := db.Query(`SELECT outline_id, source_id, title, COALESCE(summary,''), level, node_type FROM source_outlines`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	outlineCount := 0
	for rows.Next() {
		var id, sourceID, title, summary, nodeType string
		var level int
		rows.Scan(&id, &sourceID, &title, &summary, &level, &nodeType)
		idxMgr.Outlines.Index(id, map[string]interface{}{
			"outline_id": id, "source_id": sourceID,
			"title": title, "summary": summary,
			"level": level, "node_type": nodeType,
		})
		outlineCount++
	}

	// Index units
	rows2, err := db.Query(`SELECT unit_id, source_id, center, line_start, line_end FROM knowledge_units WHERE status='completed'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows2.Close()
	unitCount := 0
	for rows2.Next() {
		var id, sourceID, center string
		var lineStart, lineEnd int
		rows2.Scan(&id, &sourceID, &center, &lineStart, &lineEnd)

		content := readContent(filepath.Join(testdataDir, "markdown", sourceID+".md"), lineStart, lineEnd)
		idxMgr.Units.Index(id, map[string]interface{}{
			"unit_id": id, "source_id": sourceID, "center": center,
			"line_start": lineStart, "line_end": lineEnd, "content": content,
		})
		unitCount++
	}

	// Index points
	rows3, err := db.Query(`SELECT point_id, unit_id, source_id, content, point_type FROM knowledge_points`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows3.Close()
	pointCount := 0
	for rows3.Next() {
		var pid, uid, sid, content, ptype string
		rows3.Scan(&pid, &uid, &sid, &content, &ptype)
		idxMgr.Points.Index(pid, map[string]interface{}{
			"point_id": pid, "unit_id": uid, "source_id": sid,
			"content": content, "point_type": ptype,
		})
		pointCount++
	}

	slog.Info("indexes rebuilt", "outlines", outlineCount, "units", unitCount, "points", pointCount)
}

func readContent(path string, lineStart, lineEnd int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	return sliceLines(lines, lineStart, lineEnd)
}

func (q testQuestion) id() string { return q.ID }

func truncateQ(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
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
			t.Fatal("cannot find project root (go.mod)")
		}
		dir = parent
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("copy src: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("copy dst: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatalf("copyDir: %v", err)
	}
}
