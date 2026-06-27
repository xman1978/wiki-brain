package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestDiagSessionParser(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1")
	}

	projectRoot := findProjectRoot(t)
	cfg, err := config.Load(filepath.Join(projectRoot, "config", "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	promptsDir := filepath.Join(projectRoot, "config", "prompts")
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, promptsDir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		label          string
		input          string
		recentSubjects []string
		currentSubject string
	}{
		{"直接问题-无主题词", "出差住宿费标准是什么？", nil, ""},
		{"含制度名", "差旅费报销制度中，哪些情况可以坐飞机？", nil, ""},
		{"含制度名-长", "培训积分管理办法中，积分由哪几部分组成？", nil, ""},
		{"纯知识问题", "什么是绩效管理？", nil, ""},
		{"代词-有上下文", "它里面的考核周期是怎么规定的？", []string{"绩效管理制度"}, "绩效管理制度"},
		{"代词-无上下文", "分析它的设计思路", nil, ""},
		{"模糊意图", "看看", nil, ""},
		{"追问-有当前主题", "那伙食补贴呢？", nil, "差旅费报销制度"},
	}

	divider := strings.Repeat("─", 100)

	reportPath := filepath.Join(projectRoot, "test", "testdata", "session_diag.txt")
	f, err := os.Create(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		f.WriteString(line + "\n")
		t.Log(line)
	}

	w("Session Parser Diagnostic (subject+intent model)")
	w("Model: %s", cfg.LLM.Models["default"].Model)
	w("")

	for _, c := range cases {
		recentStr := formatRecentSubjects(c.recentSubjects)
		currentStr := truncate(c.currentSubject, 60, "（空）")
		inputStr := truncate(c.input, 200, "")

		vars := map[string]string{
			"recent_subjects": recentStr,
			"current_subject": currentStr,
			"user_input":      inputStr,
		}

		w("%s", divider)
		w("CASE: %s", c.label)
		w("  recent_subjects = %s", recentStr)
		w("  current_subject = %s", currentStr)
		w("  user_input      = %s", inputStr)

		raw, err := llmClient.Complete(context.Background(), "session_parse.md", vars, "parse")
		if err != nil {
			w("  LLM ERROR: %v", err)
			continue
		}

		w("  LLM raw output: %s", strings.TrimSpace(raw))

		result, ok := repairLayer1(raw)
		w("  Parsed: ok=%v intent=%q subject=%q", ok, result.Intent, result.Subject)

		state := &SessionState{}
		state.Working.CurrentSubject = c.currentSubject
		state.Dialogue.RecentSubjects = c.recentSubjects
		gaps := DetectGaps(result, state, c.input)
		w("  Gaps: %v → %s", gaps, func() string {
			if len(gaps) == 0 {
				return "retrieve"
			}
			return "clarify"
		}())
		w("")
	}

	t.Logf("Diagnostic: %s", reportPath)
}
