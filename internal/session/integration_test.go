package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

type sessionTestCase struct {
	ID    string        `json:"id"`
	Title string        `json:"title"`
	Turns []sessionTurn `json:"turns"`
}

type sessionTurn struct {
	Input           string `json:"input"`
	ExpectAction    string `json:"expect_action"`
	ExpectHasAnswer *bool  `json:"expect_has_answer,omitempty"`
	Note            string `json:"note,omitempty"`
}

type turnStat struct {
	caseID       string
	turnIdx      int
	input        string
	expectAction string
	actualAction string
	actionMatch  bool
	intent       string
	subject      string
	expanded     string
	answerOk     *bool
	expectAnswer *bool
	answerMatch  bool
	ansPath      string
	ansPreview   string
	elapsed      time.Duration
}

func TestIntegrationSessionFlow(t *testing.T) {
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
	q := queue.New(100)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	defer q.Shutdown()
	ansSvc := answer.NewService(ansStore, llmClient, q, retSvc)

	sessionStore := NewStore(database)
	parser := NewParser(llmClient)

	qData, err := os.ReadFile(filepath.Join(testdataDir, "session_questions.json"))
	if err != nil {
		t.Fatalf("read session questions: %v", err)
	}
	var testCases []sessionTestCase
	if err := json.Unmarshal(qData, &testCases); err != nil {
		t.Fatalf("parse session questions: %v", err)
	}

	reportPath := filepath.Join(projectRoot, "test", "testdata", "session_report.txt")
	reportFile, err := os.Create(reportPath)
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	defer reportFile.Close()

	logf := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		fmt.Fprintln(reportFile, line)
	}

	divider := strings.Repeat("=", 120)
	logf("Session Integration Report")
	logf("Generated: %s", time.Now().Format("2006-01-02 15:04:05"))
	logf("Model: %s", cfg.LLM.Models["default"].Model)
	logf("%s", divider)

	var allStats []turnStat

	for _, tc := range testCases {
		logf("")
		logf("── [%s] %s ──", tc.ID, tc.Title)

		info, err := sessionStore.CreateSession()
		if err != nil {
			t.Fatalf("[%s] create session: %v", tc.ID, err)
		}
		sid := info.SessionID

		for ti, turn := range tc.Turns {
			state, _ := sessionStore.Get(sid)
			stat := turnStat{
				caseID: tc.ID, turnIdx: ti + 1, input: turn.Input,
				expectAction: turn.ExpectAction, expectAnswer: turn.ExpectHasAnswer,
			}

			start := time.Now()
			logf("")
			logf("  Turn %d: %s", ti+1, turn.Input)

			if DetectInterrupt(turn.Input) {
				*state = SessionState{}
				sessionStore.Set(sid, state)
				sessionStore.InsertTurn(sid, turn.Input, "interrupted", "")
				stat.actualAction = "interrupted"
				stat.actionMatch = stat.actualAction == turn.ExpectAction
				stat.answerMatch = true
				stat.elapsed = time.Since(start)
				logf("    → interrupted %s", mark(stat.actionMatch))
				allStats = append(allStats, stat)
				continue
			}

			if DetectContinuation(turn.Input, state) {
				eq := Expand(state, PlanResult{Action: PlanRetrieve}, turn.Input)
				eq.ExpandedQuestion = state.Working.ContinuableAction
				sessionStore.Set(sid, state)
				sessionStore.InsertTurn(sid, turn.Input, "retrieve", "")
				stat.actualAction = "retrieve"
				stat.actionMatch = stat.actualAction == turn.ExpectAction
				stat.intent = "(continuation)"
				stat.expanded = eq.ExpandedQuestion
				stat.elapsed = time.Since(start)

				if eq.ExpandedQuestion != "" {
					ar, aerr := ansSvc.AnswerFromQuestion(context.Background(), eq.ExpandedQuestion, false)
					stat.elapsed = time.Since(start)
					aok := aerr == nil && ar != nil && ar.HasAnswer
					stat.answerOk = &aok
					if ar != nil {
						stat.ansPath = ar.Path
					}
				}
				stat.answerMatch = matchAnswer(stat.answerOk, stat.expectAnswer)
				logf("    → continuation retrieve (expanded=%s) %s", truncStr(stat.expanded, 50), mark(stat.actionMatch))
				allStats = append(allStats, stat)
				continue
			}

			parsed := parser.Parse(context.Background(), turn.Input, state)
			stat.intent = parsed.Intent
			stat.subject = parsed.Subject

			state.Dialogue.Intent = parsed.Intent
			state.Dialogue.Subject = parsed.Subject
			if parsed.Subject != "" {
				state.Working.CurrentSubject = parsed.Subject
				state.Dialogue.RecentSubjects = append(state.Dialogue.RecentSubjects, parsed.Subject)
				if len(state.Dialogue.RecentSubjects) > 3 {
					state.Dialogue.RecentSubjects = state.Dialogue.RecentSubjects[len(state.Dialogue.RecentSubjects)-3:]
				}
			}
			UpdateTopic(state)

			gaps := DetectGaps(parsed, state, turn.Input)
			plan := Plan(gaps, parsed, state)

			logf("    intent=%q subject=%q gaps=%v plan=%s", parsed.Intent, parsed.Subject, gaps, plan.Action)

			switch plan.Action {
			case PlanRetrieve:
				eq := Expand(state, plan, turn.Input)
				stat.expanded = eq.ExpandedQuestion
				sessionStore.Set(sid, state)
				sessionStore.InsertTurn(sid, turn.Input, "retrieve", "")
				stat.actualAction = "retrieve"

				ar, aerr := ansSvc.AnswerFromQuestion(context.Background(), eq.ExpandedQuestion, false)
				stat.elapsed = time.Since(start)
				aok := aerr == nil && ar != nil && ar.HasAnswer
				stat.answerOk = &aok
				if ar != nil {
					stat.ansPath = ar.Path
					stat.ansPreview = truncStr(ar.Content, 80)
					state.Working.StepSummary = "已回答"
					state.Working.ContinuableAction = eq.ExpandedQuestion
					sessionStore.Set(sid, state)
				}
				logf("    → retrieve (expanded=%s)", truncStr(eq.ExpandedQuestion, 60))
				logf("    → answer: path=%s has_answer=%v", stat.ansPath, aok)

			case PlanClarify:
				clarifyMsg := ""
				if plan.Clarification != nil {
					clarifyMsg = plan.Clarification.Question
				}
				sessionStore.Set(sid, state)
				sessionStore.InsertTurn(sid, turn.Input, "clarify", clarifyMsg)
				stat.actualAction = "clarify"
				stat.elapsed = time.Since(start)
				logf("    → clarify: %s", clarifyMsg)

			default:
				sessionStore.Set(sid, state)
				sessionStore.InsertTurn(sid, turn.Input, "retrieve", "")
				stat.actualAction = "retrieve"
				stat.elapsed = time.Since(start)
			}

			stat.actionMatch = stat.actualAction == turn.ExpectAction
			stat.answerMatch = matchAnswer(stat.answerOk, stat.expectAnswer)
			logf("    action: expect=%s actual=%s %s | answer: %s",
				turn.ExpectAction, stat.actualAction, mark(stat.actionMatch), answerSummary(stat))
			allStats = append(allStats, stat)
		}

		turns, _ := sessionStore.ListTurns(sid)
		logf("  Turns persisted: %d/%d %s", len(turns), len(tc.Turns), mark(len(turns) == len(tc.Turns)))
	}

	logf("")
	logf("%s", divider)
	logf("SUMMARY")
	logf("%s", divider)
	logf("")
	logf("  %-5s %-4s %-28s %-10s %-10s %-6s %-6s %-8s %-8s %s",
		"Case", "Turn", "Input", "ExpAction", "Action", "ActOK", "AnsOK", "Path", "Time", "Subject")

	actionPass, answerPass, total := 0, 0, 0
	for _, s := range allStats {
		total++
		if s.actionMatch {
			actionPass++
		}
		if s.answerMatch {
			answerPass++
		}
		logf("  %-5s %-4d %-28s %-10s %-10s %-6s %-6s %-8s %-8s %s",
			s.caseID, s.turnIdx, truncStr(s.input, 26),
			s.expectAction, s.actualAction, mark(s.actionMatch), answerSummary(s),
			s.ansPath, s.elapsed.Round(time.Millisecond), truncStr(s.subject, 16))
	}

	logf("")
	logf("  Action match: %d/%d (%.0f%%)", actionPass, total, pct(actionPass, total))
	logf("  Answer match: %d/%d (%.0f%%)", answerPass, total, pct(answerPass, total))

	t.Logf("Report: %s", reportPath)
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func matchAnswer(actual *bool, expect *bool) bool {
	if expect == nil {
		return true
	}
	if actual == nil {
		return false
	}
	return *actual == *expect
}

func answerSummary(s turnStat) string {
	if s.expectAnswer == nil {
		return "-"
	}
	return mark(s.answerMatch)
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
			"outline_id": id, "source_id": sourceID, "title": title, "summary": summary,
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
