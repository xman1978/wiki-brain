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

func TestEdgeCaseSessionParser(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1")
	}

	projectRoot := findProjectRoot(t)
	cfg, err := config.Load(filepath.Join(projectRoot, "config", "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, filepath.Join(projectRoot, "config", "prompts"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		label          string
		input          string
		recentSubjects []string
		currentSubject string
		expectGap      bool
	}{
		// ── 应该触发澄清的 ──
		{"纯代词-无上下文", "它是什么？", nil, "", true},
		{"指示代词-无上下文", "这个怎么理解？", nil, "", true},
		{"那个-无上下文", "那个文件讲了什么？", nil, "", true},
		{"纯动词无主题", "分析一下", nil, "", true},
		{"模糊请求", "帮我查查", nil, "", true},
		{"单字", "嗯", nil, "", true},
		{"打招呼", "你好", nil, "", true},
		{"无意义追问-无上下文", "然后呢？", nil, "", true},
		{"泛化动作-无范围", "总结一下要点", nil, "", true},
		{"比较-无对象", "两者有什么区别？", nil, "", true},

		// ── 应该直接检索的 ──
		{"完整知识问题", "什么是绩效考核？", nil, "", false},
		{"带具体主题", "住宿费报销标准是多少？", nil, "", false},
		{"长问题-自包含", "员工出差去武汉住宿费最高能报销多少？", nil, "", false},
		{"带制度名", "差旅费报销制度中交通费怎么报销？", nil, "", false},
		{"数字场景", "成本超支20%扣多少分？", nil, "", false},
		{"多关键词", "培训积分中的出勤分怎么计算？", nil, "", false},
		{"反问句式", "回款周期超过6个月由谁负责催收？", nil, "", false},

		// ── 有上下文应该直接检索的 ──
		{"代词-有当前主题", "它的适用范围是什么？", nil, "差旅费报销制度", false},
		{"追问-有当前主题", "还有其他规定吗？", nil, "培训积分管理办法", false},
		{"那个-有候选但指向不明", "那个怎么算的？", []string{"住宿费", "伙食补贴"}, "", true},
		{"省略主语-有上下文", "怎么报销？", nil, "差旅费报销制度", false},
	}

	divider := strings.Repeat("─", 110)
	reportPath := filepath.Join(projectRoot, "test", "testdata", "session_edge.txt")
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

	w("Session Edge Case Report")
	w("Model: %s", cfg.LLM.Models["default"].Model)
	w("")

	pass, fail := 0, 0

	for _, c := range cases {
		state := &SessionState{}
		state.Working.CurrentSubject = c.currentSubject
		state.Dialogue.RecentSubjects = c.recentSubjects

		vars := map[string]string{
			"recent_subjects": formatRecentSubjects(c.recentSubjects),
			"current_subject": truncate(c.currentSubject, 60, "（空）"),
			"user_input":      truncate(c.input, 200, ""),
		}

		raw, err := llmClient.Complete(context.Background(), "session_parse.md", vars, "parse")
		if err != nil {
			w("%s", divider)
			w("CASE: %s — LLM ERROR: %v", c.label, err)
			fail++
			continue
		}

		result, _ := repairLayer1(raw)
		gaps := DetectGaps(result, state, c.input)
		hasGap := len(gaps) > 0

		ok := hasGap == c.expectGap
		if ok {
			pass++
		} else {
			fail++
		}

		expect := "retrieve"
		if c.expectGap {
			expect = "clarify"
		}
		actual := "retrieve"
		if hasGap {
			actual = "clarify"
		}

		w("%s", divider)
		w("%s  %s", mark(ok), c.label)
		w("  input=%q  recent=%v  current=%q", c.input, c.recentSubjects, c.currentSubject)
		w("  LLM: intent=%q subject=%q", result.Intent, result.Subject)
		w("  expect=%s actual=%s", expect, actual)
	}

	w("")
	w("%s", strings.Repeat("=", 110))
	w("RESULT: %d/%d passed (%.0f%%)", pass, pass+fail, pct(pass, pass+fail))
	if fail > 0 {
		w("FAILURES: %d", fail)
	}

	t.Logf("Report: %s", reportPath)
}
