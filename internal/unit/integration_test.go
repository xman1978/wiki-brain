package unit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
)

func TestIntegration_SingleFile(t *testing.T) {
	if os.Getenv("WIKI_INTEGRATION") == "" {
		t.Skip("set WIKI_INTEGRATION=1 to run real LLM tests")
	}

	testFile := os.Getenv("WIKI_TEST_FILE")
	if testFile == "" {
		testFile = "../../test/44c2483c.md"
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	testFiles := []string{testFile}
	t.Run(filepath.Base(testFile), func(t *testing.T) {
		runIntegrationFiles(t, cfg, testFiles)
	})
}

func TestIntegration_RealLLM(t *testing.T) {
	if os.Getenv("WIKI_INTEGRATION") == "" {
		t.Skip("set WIKI_INTEGRATION=1 to run real LLM tests")
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	testFiles := []string{
		"../../test/5ac8ec7a.md",
		"../../test/44e1a5cc.md",
		"../../test/44c2483c.md",
	}
	runIntegrationFiles(t, cfg, testFiles)
}

func runIntegrationFiles(t *testing.T, cfg *config.Config, testFiles []string) {
	t.Helper()

	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()

	sourceStore := source.NewStore(db)
	unitStore := NewStore(db)

	promptsDir, _ := filepath.Abs("../../config/prompts")
	realClient, err := llm.NewOpenAIClient(&cfg.LLM, promptsDir)
	if err != nil {
		t.Fatalf("create LLM client: %v", err)
	}

	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "index"))
	if err != nil {
		t.Fatalf("create index: %v", err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	q := queue.New(100)
	svc := NewService(unitStore, sourceStore, realClient, idxMgr.Units, idxMgr.Points, q, cfg)

	for i, testFile := range testFiles {
		absPath, _ := filepath.Abs(testFile)
		mdBytes, err := os.ReadFile(absPath)
		if err != nil {
			t.Fatalf("read %s: %v", testFile, err)
		}
		mdLines := strings.Split(string(mdBytes), "\n")

		sourceID := fmt.Sprintf("src-%d", i+1)
		mdPath := filepath.Join(tmpDir, fmt.Sprintf("source_%d.md", i+1))
		os.WriteFile(mdPath, mdBytes, 0644)

		_, err = db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
			VALUES (?, ?, 'markdown', ?, ?, ?, 'completed')`,
			sourceID, filepath.Base(testFile), filepath.Base(testFile), absPath, mdPath)
		if err != nil {
			t.Fatalf("insert source: %v", err)
		}

		outlines := parseStructuralOutlines(mdLines, sourceID)

		// Refine oversized leaf nodes (same as source module step 6.5)
		mc := cfg.LLM.ModelForPurpose("extraction")
		newNodes, err := source.RefineLeafNodes(context.Background(), realClient, sourceID, string(mdBytes), outlines, mc, cfg.Source.SegmentMaxChars)
		if err != nil {
			t.Logf("leaf refinement failed (non-fatal): %v", err)
		}
		if len(newNodes) > 0 {
			outlines = append(outlines, newNodes...)
			t.Logf("叶节点细化: 新增 %d 个子节点", len(newNodes))
		}

		if err := sourceStore.InsertOutlines(outlines); err != nil {
			t.Fatalf("insert outlines: %v", err)
		}

		t.Logf("\n========== 文件 %d: %s ==========", i+1, filepath.Base(testFile))
		t.Logf("总行数: %d, 大纲节点: %d", len(mdLines), len(outlines))

		segments := BuildSegments(outlines, mdLines, cfg.Source.SegmentMaxChars, cfg.Source.MinSegmentChars)
		t.Logf("分段数: %d", len(segments))
		for j, seg := range segments {
			content := sliceLines(mdLines, seg.LineStart, seg.LineEnd)
			runeCount := len([]rune(content))
			t.Logf("  段 %d: 行 %d-%d (%d 字符) 标题=%q", j+1, seg.LineStart, seg.LineEnd, runeCount, seg.Title)
		}

		t.Logf("--- 开始 LLM 提取 ---")
		err = svc.Extract(context.Background(), sourceID)
		if err != nil {
			t.Errorf("extract %s failed: %v", testFile, err)
			continue
		}

		units, _ := unitStore.GetUnitsBySourceID(sourceID)
		t.Logf("提取结果: %d 个知识单元", len(units))
		for _, u := range units {
			t.Logf("  [%s] %s (行 %d-%d, 状态: %s)", u.UnitID[:8], u.Center, u.LineStart, u.LineEnd, u.Status)
			if u.Status == "extraction_failed" && u.ErrorMsg.Valid {
				t.Logf("    错误: %s", u.ErrorMsg.String)
			}
			points, _ := unitStore.GetPointsByUnitID(u.UnitID)
			for _, p := range points {
				t.Logf("    - [%s] %s", p.PointType, p.Content)
			}
		}

		allPoints, _ := unitStore.GetPointsBySourceID(sourceID)
		t.Logf("知识点总数: %d", len(allPoints))

		for _, p := range allPoints {
			rels, _ := unitStore.GetRelationsByPointID(p.PointID)
			if len(rels) > 0 {
				t.Logf("  KPN 关系 (%s): %d 条", p.Content[:min(20, len(p.Content))], len(rels))
			}
		}
	}
}

func parseStructuralOutlines(lines []string, sourceID string) []source.Outline {
	type heading struct {
		level     int
		title     string
		lineStart int
	}

	var headings []heading
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for _, c := range trimmed {
			if c == '#' {
				level++
			} else {
				break
			}
		}
		if level > 0 && level <= 6 {
			title := strings.TrimSpace(trimmed[level:])
			if title != "" {
				headings = append(headings, heading{level: level, title: title, lineStart: i + 1})
			}
		}
	}

	if len(headings) == 0 {
		return []source.Outline{{
			SourceID:  sourceID,
			Level:     1,
			Title:     "全文",
			LineStart: 1,
			LineEnd:   len(lines),
			NodeType:  "structural",
			Position:  1,
		}}
	}

	var outlines []source.Outline
	for i, h := range headings {
		lineEnd := len(lines)
		if i+1 < len(headings) {
			lineEnd = headings[i+1].lineStart - 1
		}
		if lineEnd < h.lineStart {
			lineEnd = h.lineStart
		}

		var parentID sql.NullString
		for j := i - 1; j >= 0; j-- {
			if headings[j].level < h.level {
				parentID = sql.NullString{String: fmt.Sprintf("ol-%s-%d", sourceID, j), Valid: true}
				break
			}
		}

		outlines = append(outlines, source.Outline{
			OutlineID: fmt.Sprintf("ol-%s-%d", sourceID, i),
			SourceID:  sourceID,
			ParentID:  parentID,
			Level:     h.level,
			Title:     h.title,
			LineStart: h.lineStart,
			LineEnd:   lineEnd,
			NodeType:  "structural",
			Position:  i + 1,
		})
	}

	return outlines
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
