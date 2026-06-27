package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '',
			state_snapshot TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE session_turns (
			turn_id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
			turn_index INTEGER NOT NULL, user_input TEXT NOT NULL, action TEXT NOT NULL,
			answer_id TEXT, clarify_msg TEXT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// --- InterruptDetector ---

func TestDetectInterrupt(t *testing.T) {
	for _, kw := range []string{"停", "停下", "清空", "重来", "不用了"} {
		if !DetectInterrupt(kw) {
			t.Errorf("expected interrupt for %q", kw)
		}
	}
	if DetectInterrupt("出差住宿费标准是什么") {
		t.Error("should not trigger interrupt for normal input")
	}
}

// --- ContinuationDetector ---

func TestDetectContinuation(t *testing.T) {
	state := &SessionState{}
	state.Working.ContinuableAction = "查询住宿费标准"

	for _, kw := range []string{"继续", "下一个", "按刚才"} {
		if !DetectContinuation(kw, state) {
			t.Errorf("expected continuation for %q", kw)
		}
	}

	empty := &SessionState{}
	if DetectContinuation("继续", empty) {
		t.Error("should not trigger continuation when continuable_action is empty")
	}
}

// --- GapDetector ---

func TestDetectGaps(t *testing.T) {
	tests := []struct {
		name    string
		parsed  ParseResult
		current string
		input   string
		hasGap  bool
	}{
		{"subject present", ParseResult{Intent: "查询标准", Subject: "住宿费"}, "", "住宿费标准是什么", false},
		{"current_subject fills gap", ParseResult{Intent: "查询标准", Subject: ""}, "差旅制度", "标准是什么", false},
		{"no subject no current", ParseResult{Intent: "查询标准", Subject: ""}, "", "标准是什么", true},
		{"vague intent no subject", ParseResult{Intent: "看看", Subject: ""}, "", "看看", true},
		{"dangling pronoun", ParseResult{Intent: "分析设计思路", Subject: ""}, "", "分析它的设计思路", true},
		{"no scope", ParseResult{Intent: "分析设计思路", Subject: ""}, "", "分析设计思路", true},
		{"pronoun resolved by current", ParseResult{Intent: "分析设计思路", Subject: ""}, "绩效管理制度", "分析它的设计思路", false},
		{"empty everything", ParseResult{Intent: "", Subject: ""}, "", "嗯", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &SessionState{}
			state.Working.CurrentSubject = tt.current
			gaps := DetectGaps(tt.parsed, state, tt.input)
			if tt.hasGap && len(gaps) == 0 {
				t.Error("expected gap")
			}
			if !tt.hasGap && len(gaps) > 0 {
				t.Errorf("unexpected gap: %v", gaps)
			}
		})
	}
}

// --- Layer1 Repair ---

func TestRepairLayer1(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		intent  string
		subject string
		ok      bool
	}{
		{"clean", `{"intent":"查询标准","subject":"住宿费"}`, "查询标准", "住宿费", true},
		{"extra_text", `好的 {"intent":"查询标准","subject":"住宿费"} 完毕`, "查询标准", "住宿费", true},
		{"empty_subject", `{"intent":"查询标准","subject":""}`, "查询标准", "", true},
		{"no_json", `我不知道`, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := repairLayer1(tt.raw)
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && result.Intent != tt.intent {
				t.Errorf("intent = %q, want %q", result.Intent, tt.intent)
			}
			if ok && result.Subject != tt.subject {
				t.Errorf("subject = %q, want %q", result.Subject, tt.subject)
			}
		})
	}
}

// --- SessionStore ---

func TestStoreGetSetDelete(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)

	info, err := store.CreateSession()
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Get(info.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dialogue.Intent != "" {
		t.Error("expected empty intent for new session")
	}

	st.Dialogue.Intent = "查询标准"
	st.Dialogue.Subject = "住宿费"
	st.Working.CurrentSubject = "住宿费"
	if err := store.Set(info.SessionID, st); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	delete(store.cache, info.SessionID)
	store.mu.Unlock()

	st2, err := store.Get(info.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Dialogue.Intent != "查询标准" {
		t.Errorf("intent = %q, want 查询标准", st2.Dialogue.Intent)
	}
	if st2.Working.CurrentSubject != "住宿费" {
		t.Errorf("current_subject = %q, want 住宿费", st2.Working.CurrentSubject)
	}

	if err := store.Delete(info.SessionID); err != nil {
		t.Fatal(err)
	}
}

// --- Planner ---

func TestPlanNoGap(t *testing.T) {
	parsed := ParseResult{Intent: "查询标准", Subject: "住宿费"}
	result := Plan(nil, parsed, &SessionState{})
	if result.Action != PlanRetrieve {
		t.Errorf("action = %v, want retrieve", result.Action)
	}
	if result.Subject != "住宿费" {
		t.Errorf("subject = %q, want 住宿费", result.Subject)
	}
}

func TestPlanVagueWithRecent(t *testing.T) {
	gaps := []GapKind{GapVague}
	state := &SessionState{}
	state.Dialogue.RecentSubjects = []string{"住宿费", "伙食补贴"}
	result := Plan(gaps, ParseResult{Intent: "看看"}, state)
	if result.Action != PlanClarify {
		t.Errorf("action = %v, want clarify", result.Action)
	}
	if len(result.Clarification.Options) != 2 {
		t.Errorf("options = %d, want 2", len(result.Clarification.Options))
	}
}

func TestPlanVagueNoRecent(t *testing.T) {
	gaps := []GapKind{GapVague}
	result := Plan(gaps, ParseResult{Intent: "看看"}, &SessionState{})
	if result.Action != PlanClarify {
		t.Errorf("action = %v, want clarify", result.Action)
	}
	if len(result.Clarification.Options) != 0 {
		t.Errorf("options = %d, want 0", len(result.Clarification.Options))
	}
}

// --- HTTP Handler ---

func TestHTTPCreateAndListSessions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	h := NewHandler(store, NewParser(llm.NewFakeClient()))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/sessions", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
}

func TestHTTPTurnInterrupt(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	h := NewHandler(store, NewParser(llm.NewFakeClient()))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created SessionInfo
	json.NewDecoder(w.Body).Decode(&created)

	body := `{"session_id":"` + created.SessionID + `","user_input":"停下"}`
	req = httptest.NewRequest("POST", "/session/turn", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var result TurnResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Action != "interrupted" {
		t.Errorf("action = %q, want interrupted", result.Action)
	}
}

func TestHTTPTurnWithLLM(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	fake := llm.NewFakeClient()
	fake.SetResponse("session_parse.md", llm.FakeResponse{
		Output: `{"intent":"查询住宿费标准","subject":"差旅费报销制度"}`,
	})
	h := NewHandler(store, NewParser(fake))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created SessionInfo
	json.NewDecoder(w.Body).Decode(&created)

	body := `{"session_id":"` + created.SessionID + `","user_input":"差旅费报销制度中住宿费标准是什么"}`
	req = httptest.NewRequest("POST", "/session/turn", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var result TurnResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Action != "retrieve" {
		t.Errorf("action = %q, want retrieve", result.Action)
	}
	if result.ExpandedQuery == nil {
		t.Fatal("expected expanded_query")
	}
}

func TestHTTPTurnDirectQuestion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	fake := llm.NewFakeClient()
	fake.SetResponse("session_parse.md", llm.FakeResponse{
		Output: `{"intent":"查询住宿费标准","subject":"出差住宿费"}`,
	})
	h := NewHandler(store, NewParser(fake))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created SessionInfo
	json.NewDecoder(w.Body).Decode(&created)

	body := `{"session_id":"` + created.SessionID + `","user_input":"出差住宿费最高能报多少"}`
	req = httptest.NewRequest("POST", "/session/turn", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var result TurnResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Action != "retrieve" {
		t.Errorf("action = %q, want retrieve (direct question should not trigger clarify)", result.Action)
	}
}

func TestHTTPContinuation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	h := NewHandler(store, NewParser(llm.NewFakeClient()))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created SessionInfo
	json.NewDecoder(w.Body).Decode(&created)

	body := `{"session_id":"` + created.SessionID + `","step_summary":"已分析","continuable_action":"查询住宿费标准"}`
	req = httptest.NewRequest("POST", "/session/working", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body = `{"session_id":"` + created.SessionID + `","user_input":"继续"}`
	req = httptest.NewRequest("POST", "/session/turn", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var result TurnResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Action != "retrieve" {
		t.Errorf("action = %q, want retrieve", result.Action)
	}
	if result.ExpandedQuery.ExpandedQuestion != "查询住宿费标准" {
		t.Errorf("expanded = %q, want 查询住宿费标准", result.ExpandedQuery.ExpandedQuestion)
	}
}

func TestHTTPDeleteCascade(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db)
	fake := llm.NewFakeClient()
	fake.SetResponse("session_parse.md", llm.FakeResponse{
		Output: `{"intent":"查看","subject":"x.md"}`,
	})
	h := NewHandler(store, NewParser(fake))

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created SessionInfo
	json.NewDecoder(w.Body).Decode(&created)

	body := `{"session_id":"` + created.SessionID + `","user_input":"查看 x.md"}`
	req = httptest.NewRequest("POST", "/session/turn", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	req = httptest.NewRequest("DELETE", "/sessions/"+created.SessionID, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM session_turns WHERE session_id = ?", created.SessionID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 turns after delete, got %d", count)
	}
}

func TestSessionParserRetry(t *testing.T) {
	fake := llm.NewFakeClient()
	fake.SetResponse("session_parse.md", llm.FakeResponse{Output: "无效输出"})
	fake.SetResponse("session_retry_intent.md", llm.FakeResponse{Output: "查询标准"})

	parser := NewParser(fake)
	state := &SessionState{}
	result := parser.Parse(context.Background(), "住宿费标准是什么", state)

	if result.Intent != "查询标准" {
		t.Errorf("intent = %q, want 查询标准", result.Intent)
	}
}
