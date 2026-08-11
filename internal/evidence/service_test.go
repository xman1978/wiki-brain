package evidence

import (
	"context"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func testConfig() config.EvidenceConfig {
	return config.EvidenceConfig{
		Enabled:           true,
		BatchMaxChars:     6000,
		MaxFragmentsPerKU: 5,
		MinFragmentChars:  4,
		Retry:             1,
	}
}

func TestMine_Disabled_ReturnsInputUnchanged(t *testing.T) {
	fake := llm.NewFakeClient()
	cfg := testConfig()
	cfg.Enabled = false
	svc := NewService(fake, cfg)

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: "some KU content", Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 1 || out[0].Content != "some KU content" || out[0].Mined {
		t.Fatalf("expected passthrough of input, got %+v", out)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls when disabled, got %d", len(fake.Calls()))
	}
}

func TestMine_NormalPath_ProducesFragmentLevelEvidence(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "背景介绍。\n实际步骤是先做A再做B。\n结尾说明。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["实际步骤是先做A再做B。"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", SourceID: "s1", LineStart: 10, LineEnd: 12, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 1 {
		t.Fatalf("expected 1 fragment, got %d: %+v", len(out), out)
	}
	frag := out[0]
	if !frag.Mined {
		t.Error("expected mined=true")
	}
	if frag.Content != "实际步骤是先做A再做B。" {
		t.Errorf("content = %q", frag.Content)
	}
	if frag.UnitID != "u1" || frag.PointID != "p1" || frag.SourceID != "s1" {
		t.Errorf("expected inherited unit_id/point_id/source_id, got %+v", frag)
	}
	// content is line 2 of the KU (relative), KU starts at absolute line 10.
	if frag.LineStart != 11 || frag.LineEnd != 11 {
		t.Errorf("line_start/line_end = %d/%d, want 11/11", frag.LineStart, frag.LineEnd)
	}
}

func TestMine_HallucinatedFragment_Dropped(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "the actual KU content about topic X"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["this text does not exist in the KU"]}]}`,
	})

	// role=supporting so a fully-empty mining result drops the candidate,
	// making it easy to assert nothing hallucinated leaked through.
	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: content, Role: RoleSupporting},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 0 {
		t.Fatalf("expected hallucinated fragment to be dropped entirely, got %+v", out)
	}
}

func TestMine_EmptyFragments_DirectFallsBackWholeSegment(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "direct candidate content"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": []}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", LineStart: 1, LineEnd: 3, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 1 {
		t.Fatalf("expected whole-segment fallback item, got %+v", out)
	}
	if out[0].Mined {
		t.Error("expected mined=false for whole-segment fallback")
	}
	if out[0].Content != content {
		t.Errorf("expected original KU content preserved, got %q", out[0].Content)
	}
	if out[0].LineStart != 1 || out[0].LineEnd != 3 {
		t.Errorf("expected original line range preserved, got %d-%d", out[0].LineStart, out[0].LineEnd)
	}
}

func TestMine_EmptyFragments_SupportingDropped(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": []}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: "supporting content", Role: RoleSupporting},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 0 {
		t.Fatalf("expected supporting candidate with no fragments to be dropped, got %+v", out)
	}
}

// TestMine_EmptyFragments_SupportingFallsBackWhenLastResort covers
// retrieval's last retry attempt (docs/impl/v1/retrieval.md 空结果重试链路的
// 最后一环): when nothing else is left to try, a supporting candidate that
// mines nothing gets the same whole-segment fallback as direct, instead of
// being silently dropped into an empty EvidenceSet.
func TestMine_EmptyFragments_SupportingFallsBackWhenLastResort(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "supporting content"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": []}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", LineStart: 1, LineEnd: 3, Content: content, Role: RoleSupporting},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, true)

	if len(out) != 1 {
		t.Fatalf("expected whole-segment fallback item, got %+v", out)
	}
	if out[0].Mined {
		t.Error("expected mined=false for whole-segment fallback")
	}
	if out[0].Content != content {
		t.Errorf("expected original KU content preserved, got %q", out[0].Content)
	}
}

func TestMine_BatchFailure_WholeSegmentFallbackAfterRetries(t *testing.T) {
	fake := llm.NewFakeClient()
	cfg := testConfig()
	cfg.Retry = 2
	svc := NewService(fake, cfg)

	fake.SetResponse("evidence_mine.md", llm.FakeResponse{Output: `not valid json`})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: "content one", Role: RoleDirect},
		{UnitID: "u2", PointID: "p2", Content: "content two", Role: RoleSupporting},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 2 {
		t.Fatalf("expected both candidates to whole-segment fallback (batch failure applies regardless of role), got %+v", out)
	}
	for _, item := range out {
		if item.Mined {
			t.Errorf("expected mined=false after batch failure, got %+v", item)
		}
	}

	// retry=2 means 3 total attempts.
	if len(fake.Calls()) != 3 {
		t.Errorf("expected 3 LLM attempts (1 + retry=2), got %d", len(fake.Calls()))
	}
}

func TestMine_MissingCandidateCoverage_TreatedAsBatchFailure(t *testing.T) {
	fake := llm.NewFakeClient()
	cfg := testConfig()
	cfg.Retry = 0
	svc := NewService(fake, cfg)

	// Only covers c1, not c2 — violates "results 覆盖批内全部 candidate_id".
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["content one"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: "content one", Role: RoleDirect},
		{UnitID: "u2", PointID: "p2", Content: "content two", Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 2 {
		t.Fatalf("expected whole-batch fallback due to incomplete coverage, got %+v", out)
	}
	for _, item := range out {
		if item.Mined {
			t.Errorf("expected mined=false, got %+v", item)
		}
	}
}

func TestMine_TruncatesToMaxFragmentsPerKU(t *testing.T) {
	fake := llm.NewFakeClient()
	cfg := testConfig()
	cfg.MaxFragmentsPerKU = 2
	svc := NewService(fake, cfg)

	// One fragment per line — line-range dedup (docs/impl/v1/evidence.md 步骤 3.6)
	// is intentionally coarse (foundation.md forbids byte/char-offset positions),
	// so distinct fragments sharing one line would otherwise collide.
	content := "frag-alpha here.\nfrag-beta here.\nfrag-gamma here.\nfrag-delta here."
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["frag-alpha here.", "frag-beta here.", "frag-gamma here.", "frag-delta here."]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 2 {
		t.Fatalf("expected truncation to max_fragments_per_ku=2, got %d: %+v", len(out), out)
	}
	if out[0].Content != "frag-alpha here." || out[1].Content != "frag-beta here." {
		t.Errorf("expected first 2 fragments in appearance order, got %+v", out)
	}
}

func TestMine_DedupesOverlappingLineRanges(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "the important fact is here."
	// Same fragment text yields the same line range twice.
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["the important fact is here.", "the important fact is here."]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 1 {
		t.Fatalf("expected exact-overlapping duplicate dropped, got %d: %+v", len(out), out)
	}
}

func TestMine_MinFragmentChars_DropsShortFragments(t *testing.T) {
	fake := llm.NewFakeClient()
	cfg := testConfig()
	cfg.MinFragmentChars = 10
	svc := NewService(fake, cfg)

	content := "ok a real longer supporting fragment goes here"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["ok", "a real longer supporting fragment goes here"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "q", "s", "i", in, false)

	if len(out) != 1 {
		t.Fatalf("expected short fragment 'ok' dropped, got %+v", out)
	}
	if out[0].Content != "a real longer supporting fragment goes here" {
		t.Errorf("unexpected surviving fragment: %q", out[0].Content)
	}
}

// TestMine_TableDataRowFragment_WidensToWholeTable is the regression test
// for the real-world bug this was written to fix: mining picked only the
// data row "全体员工 | 350元 | 280元 | 220元 | 200元" out of a category-as-
// columns table, with no header row saying which number is A/B/C/D class —
// downstream, an answer about a C类城市 guessed 350 (the first column)
// instead of the correct 220. The fragment must be widened to the table's
// full contiguous range (header + separator + every data row it touches).
func TestMine_TableDataRowFragment_WidensToWholeTable(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "住宿限额标准如下：\n" +
		"| 分类 | A 类城市 | B 类城市 | C 类城市 | D 类城市 |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| 全体员工 | 350元 | 280元 | 220元 | 200元 |\n" +
		"超出部分自理。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["全体员工 | 350元 | 280元 | 220元 | 200元"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", SourceID: "s1", LineStart: 67, LineEnd: 71, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "福州属于哪一类城市，报销标准是多少？", "出差报销", "查询报销标准", in, false)

	if len(out) != 1 {
		t.Fatalf("expected 1 widened fragment, got %d: %+v", len(out), out)
	}
	frag := out[0]
	wantContent := "住宿限额标准如下：\n" +
		"| 分类 | A 类城市 | B 类城市 | C 类城市 | D 类城市 |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| 全体员工 | 350元 | 280元 | 220元 | 200元 |"
	if frag.Content != wantContent {
		t.Errorf("content = %q, want lead-in sentence + header+separator+data row all included:\n%q", frag.Content, wantContent)
	}
	// the table's own lead-in sentence ("住宿限额标准如下：") is now attached
	// too, so the widened fragment starts at relative line 1 -> absolute 67.
	if frag.LineStart != 67 || frag.LineEnd != 70 {
		t.Errorf("line_start/line_end = %d/%d, want 67/70 (widened to lead-in + whole table, not the trailing prose line)", frag.LineStart, frag.LineEnd)
	}
}

func TestMine_LeadInSentenceOnly_AttachesFollowingTable(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "一级考评者只对员工绩效进行评分，不做排序和定级。\n" +
		"员工绩效考核结果划分为 S、A、B、C、D 五个等级：\n" +
		"| 等级 | 对应分数 | 比例 |\n" +
		"| --- | --- | --- |\n" +
		"| S | 95-100 | 0-5% |\n" +
		"| A | 90-95 | 10%-15% |\n" +
		"原则上，绩效等级结果应遵循正态分布规律。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["员工绩效考核结果划分为 S、A、B、C、D 五个等级："]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", SourceID: "s1", LineStart: 111, LineEnd: 117, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "绩效考核分几个等级、S 级比例多少", "绩效考核", "查询考核等级与比例", in, false)

	if len(out) != 1 {
		t.Fatalf("expected 1 widened fragment, got %d: %+v", len(out), out)
	}
	frag := out[0]
	wantContent := "员工绩效考核结果划分为 S、A、B、C、D 五个等级：\n" +
		"| 等级 | 对应分数 | 比例 |\n" +
		"| --- | --- | --- |\n" +
		"| S | 95-100 | 0-5% |\n" +
		"| A | 90-95 | 10%-15% |"
	if frag.Content != wantContent {
		t.Errorf("content = %q, want lead-in sentence + whole following table:\n%q", frag.Content, wantContent)
	}
}

func TestExpandToTableBlock(t *testing.T) {
	lines := []string{
		"介绍文字",          // 1
		"| a | b |",     // 2
		"| --- | --- |", // 3
		"| 1 | 2 |",     // 4
		"| 3 | 4 |",     // 5
		"表格之后的说明文字",     // 6
	}

	t.Run("widens a lone data row to the whole table", func(t *testing.T) {
		start, end := expandToTableBlock(lines, 5, 5)
		if start != 2 || end != 5 {
			t.Errorf("got %d-%d, want 2-5", start, end)
		}
	})

	t.Run("non-table fragment is left untouched", func(t *testing.T) {
		start, end := expandToTableBlock(lines, 1, 1)
		if start != 1 || end != 1 {
			t.Errorf("got %d-%d, want unchanged 1-1", start, end)
		}
		start, end = expandToTableBlock(lines, 6, 6)
		if start != 6 || end != 6 {
			t.Errorf("got %d-%d, want unchanged 6-6", start, end)
		}
	})

	t.Run("fragment already spanning the whole table is unchanged", func(t *testing.T) {
		start, end := expandToTableBlock(lines, 2, 5)
		if start != 2 || end != 5 {
			t.Errorf("got %d-%d, want unchanged 2-5", start, end)
		}
	})
}

// TestMine_PartialSQL_WidensToCommandBlock: mining returned only the alter
// system line; program must fold the contiguous shell/SQL run so LMS* and
// the preceding sqlplus login stay in the fragment (T10-class failure).
func TestMine_PartialSQL_WidensToCommandBlock(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "虚拟化环境下 VKTM 占用过高。\n" +
		"$ su - oracle\n" +
		"$ sqlplus / as sysdba\n" +
		"SQL> alter system set \"_high_priority_processes\"='LMS*' scope=spfile;\n" +
		"不建议用于生产环境。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["SQL> alter system set \"_high_priority_processes\"='LMS*' scope=spfile;"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", SourceID: "s1", LineStart: 10, LineEnd: 14, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "VKTM CPU 高怎么处理？", "Oracle RAC", "排查", in, false)
	if len(out) != 1 {
		t.Fatalf("expected 1 widened fragment, got %d: %+v", len(out), out)
	}
	want := "虚拟化环境下 VKTM 占用过高。\n" +
		"$ su - oracle\n" +
		"$ sqlplus / as sysdba\n" +
		"SQL> alter system set \"_high_priority_processes\"='LMS*' scope=spfile;"
	if out[0].Content != want {
		t.Errorf("content = %q\nwant %q", out[0].Content, want)
	}
	if !strings.Contains(out[0].Content, "LMS*") {
		t.Errorf("widened fragment must keep LMS* assignment")
	}
}

// TestMine_PartialChmod_WidensToCommandBlock covers T11-class: a lone chmod
// line in a short command run stays complete (assignment + path).
func TestMine_PartialChmod_WidensToCommandBlock(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "原因是 oracle 可执行文件缺少粘连位。\n" +
		"chmod u+s /u01/app/oracle/product/11.2.0/db/bin/oracle\n" +
		"处理后重新连接验证。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["chmod u+s /u01/app/oracle/product/11.2.0/db/bin/oracle"]}]}`,
	})
	in := []EvidenceItem{
		{UnitID: "u1", LineStart: 1, LineEnd: 3, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "TNS-12518 原因？", "Oracle", "排查", in, false)
	if len(out) != 1 {
		t.Fatalf("got %d items: %+v", len(out), out)
	}
	want := "原因是 oracle 可执行文件缺少粘连位。\n" +
		"chmod u+s /u01/app/oracle/product/11.2.0/db/bin/oracle"
	if out[0].Content != want {
		t.Errorf("content = %q, want preceding sentence + command:\n%q", out[0].Content, want)
	}
}

// TestMine_PartialFencedCode_WidensToWholeFence covers ``` blocks.
func TestMine_PartialFencedCode_WidensToWholeFence(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "示例配置：\n" +
		"```\n" +
		"BUFFER=10000\n" +
		"MAX_BUFFER=10000\n" +
		"```\n" +
		"说明文字。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["BUFFER=10000"]}]}`,
	})
	in := []EvidenceItem{
		{UnitID: "u1", LineStart: 1, LineEnd: 6, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "BUFFER 怎么配？", "达梦", "查询", in, false)
	if len(out) != 1 {
		t.Fatalf("got %d: %+v", len(out), out)
	}
	want := "示例配置：\n```\nBUFFER=10000\nMAX_BUFFER=10000\n```"
	if out[0].Content != want {
		t.Errorf("got %q want %q", out[0].Content, want)
	}
}

// TestMine_ConfigBlockOnly_AttachesPrecedingGuideSentence is the regression
// test for the "Oracle RAC 两个实例必须保持一致的参数有哪些" bug: mining
// picked only the bare parameter assignments, leaving out the guiding
// sentence above them that says what the block actually means ("必须保持
// 一致的参数"), so the answer step couldn't tell the config dump answered
// the question at all.
func TestMine_ConfigBlockOnly_AttachesPrecedingGuideSentence(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "3. oracle RAC 实例必须保持一致的参数\n" +
		"cluster_database_instances=2\n" +
		"cluster_database=true\n" +
		"compatible='11.2.0.4.0'"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["cluster_database_instances=2\ncluster_database=true\ncompatible='11.2.0.4.0'"]}]}`,
	})

	in := []EvidenceItem{
		{UnitID: "u1", PointID: "p1", SourceID: "s1", LineStart: 42, LineEnd: 45, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "Oracle RAC 两个实例必须保持一致的参数有哪些", "Oracle RAC", "查询一致性参数", in, false)

	if len(out) != 1 {
		t.Fatalf("expected 1 widened fragment, got %d: %+v", len(out), out)
	}
	want := "3. oracle RAC 实例必须保持一致的参数\n" +
		"cluster_database_instances=2\n" +
		"cluster_database=true\n" +
		"compatible='11.2.0.4.0'"
	if out[0].Content != want {
		t.Errorf("content = %q, want guide sentence + config block:\n%q", out[0].Content, want)
	}
}

// TestMine_HeadingOnlyFragment_Dropped: a bare markdown heading is not
// evidence (T18-class UDEV title stub). Direct candidate then whole-segments.
func TestMine_HeadingOnlyFragment_Dropped(t *testing.T) {
	fake := llm.NewFakeClient()
	svc := NewService(fake, testConfig())

	content := "磁盘绑定有两种方式。\n" +
		"# 二、UDEV 磁盘映射\n" +
		"通过 udev rules 绑定 SCSI 序列号。\n" +
		"也可使用 AFD。"
	fake.SetResponse("evidence_mine.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "fragments": ["# 二、UDEV 磁盘映射"]}]}`,
	})
	in := []EvidenceItem{
		{UnitID: "u1", LineStart: 1, LineEnd: 4, Content: content, Role: RoleDirect},
	}
	out := svc.Mine(context.Background(), "有哪些磁盘绑定方式？", "Oracle", "列举", in, false)
	if len(out) != 1 {
		t.Fatalf("expected whole-segment fallback after heading drop, got %d: %+v", len(out), out)
	}
	if out[0].Mined {
		t.Errorf("expected mined=false whole-segment fallback, got mined=true content=%q", out[0].Content)
	}
	if out[0].Content != content {
		t.Errorf("fallback should keep full KU, got %q", out[0].Content)
	}
}

func TestExpandToCommandConfigBlock(t *testing.T) {
	lines := []string{
		"说明",                                            // 1
		"$ su - oracle",                                // 2
		"SQL> alter system set \"_high_priority_processes\"='LMS*' scope=spfile;", // 3
		"注意：不建议生产",                                   // 4
	}
	start, end, ok := expandToCommandConfigBlock(lines, 3, 3)
	if !ok || start != 2 || end != 3 {
		t.Errorf("got ok=%v %d-%d, want 2-3", ok, start, end)
	}
	start, end, ok = expandToCommandConfigBlock(lines, 1, 1)
	if ok {
		t.Errorf("prose should not expand, got %d-%d", start, end)
	}
}

func TestIsHeadingOnlyFragment(t *testing.T) {
	lines := []string{"# 二、UDEV 磁盘映射", "正文"}
	if !isHeadingOnlyFragment(lines, 1, 1) {
		t.Error("single heading should be heading-only")
	}
	if isHeadingOnlyFragment(lines, 1, 2) {
		t.Error("heading+body is not heading-only")
	}
}

func TestBatchCandidates_SplitsBySize(t *testing.T) {
	candidates := []EvidenceItem{
		{UnitID: "u1", Content: "1234567890"}, // 10 chars
		{UnitID: "u2", Content: "1234567890"}, // 10 chars
		{UnitID: "u3", Content: "1234567890"}, // 10 chars
	}
	batches := batchCandidates(candidates, 15)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches (2 per batch would overflow 15), got %d: %+v", len(batches), batches)
	}
}

func TestBatchCandidates_OversizedCandidateGetsOwnBatch(t *testing.T) {
	candidates := []EvidenceItem{
		{UnitID: "u1", Content: "short"},
		{UnitID: "u2", Content: "this-content-is-way-too-long-for-the-limit"},
		{UnitID: "u3", Content: "short2"},
	}
	batches := batchCandidates(candidates, 10)
	if len(batches) != 3 {
		t.Fatalf("expected oversized KU to get its own batch, got %d batches: %+v", len(batches), batches)
	}
	if len(batches[1]) != 1 || batches[1][0].UnitID != "u2" {
		t.Errorf("expected oversized candidate alone in its batch, got %+v", batches[1])
	}
}
