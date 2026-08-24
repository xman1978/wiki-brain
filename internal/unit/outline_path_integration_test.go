//go:build integration

package unit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/llmconfig"
)

// TestIntegration_PointExtractMealSubsidyUsesOutlinePath reproduces the
// 差旅费报销制度 failure: L99-105 is only "(二)补贴标准" + a hotel/dorm table,
// with no "第七条伙食补贴" heading in the unit text. With the full outline
// path, center/KP must stay 伙食补贴, not invent 住宿补贴.
func TestIntegration_PointExtractMealSubsidyUsesOutlinePath(t *testing.T) {
	cfg, err := config.Load("../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	llmClient, err := llmconfig.NewRoutingFromBootstrap(cfg.BootstrapLLM, "../../config/prompts")
	if err != nil {
		t.Fatalf("llm client: %v", err)
	}

	mdPath := filepath.Join("..", "..", "data20260822", "sources", "markdown", "4950596c-f9dc-4225-99a4-ac17a1a9c0a5.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read real markdown: %v", err)
	}
	mdLines := strings.Split(string(raw), "\n")

	outlinePath := "第三章出差期间发生的住宿费、交通费与伙食补贴 / 伙食补贴计算方式及分级标准"
	unitContent := sliceLinesWithLineNumbers(mdLines, 99, 105)
	if !strings.Contains(unitContent, "补贴标准") || !strings.Contains(unitContent, "酒店") {
		t.Fatalf("unexpected unit content for L99-105:\n%s", unitContent)
	}
	if strings.Contains(unitContent, "伙食") {
		t.Fatal("fixture invalid: unit text already contains 伙食 — cannot verify outline anchoring")
	}

	svc := &Service{llmClient: llmClient}
	u := llmUnit{UnitID: "u1", LineStart: 99, LineEnd: 105}
	sourceSummary := "本文档为差旅费报销制度，涵盖总则、交通与住宿标准、费用补贴及报销流程等内容。适用范围为公司内部员工出差期间的住宿费、交通费及伙食补贴管理。"

	center, points, ok, err := svc.extractPointsForSplitUnit(
		context.Background(),
		"差旅费报销制度",
		sourceSummary,
		outlinePath,
		u,
		unitContent,
	)
	if err != nil {
		t.Fatalf("extractPointsForSplitUnit: %v", err)
	}
	if !ok || len(points) == 0 {
		t.Fatalf("expected points, ok=%v points=%d", ok, len(points))
	}

	t.Logf("center=%q", center)
	for i, p := range points {
		t.Logf("point[%d] theme=%q content=%q", i, p.ContentTheme, p.Content)
	}

	joined := center
	for _, p := range points {
		joined += "\n" + p.Content + "\n" + p.ContentTheme
	}
	if !strings.Contains(joined, "伙食") {
		t.Fatalf("expected 伙食 in center/KP given outline path %q, got:\n%s", outlinePath, joined)
	}
	if strings.Contains(joined, "住宿补贴") || strings.Contains(joined, "酒店住宿") {
		t.Fatalf("must not mislabel as 住宿补贴 when outline is 伙食补贴; got:\n%s", joined)
	}
}
