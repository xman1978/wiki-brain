package unit

import (
	"testing"
)

func TestRangeRelation(t *testing.T) {
	tests := []struct {
		name                       string
		aStart, aEnd, bStart, bEnd int
		want                       string
	}{
		{"identical spans", 5, 10, 5, 10, RangeExact},
		{"a contains b", 5, 20, 8, 12, RangeContains},
		{"b contains a", 8, 12, 5, 20, RangeContains},
		{"contains sharing start", 5, 20, 5, 10, RangeContains},
		{"partial overlap", 5, 10, 8, 15, RangeOverlap},
		{"touching end-to-start counts as nearby", 5, 10, 11, 15, RangeNearby},
		{"gap within dedupMaxGapLines", 5, 10, 10 + 1 + dedupMaxGapLines, 20, RangeNearby},
		{"gap beyond dedupMaxGapLines", 5, 10, 10 + 2 + dedupMaxGapLines, 20, RangeDistant},
		{"b before a distant", 30, 40, 5, 10, RangeDistant},
		{"b before a nearby", 12, 20, 5, 10, RangeNearby},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rangeRelation(tt.aStart, tt.aEnd, tt.bStart, tt.bEnd); got != tt.want {
				t.Errorf("rangeRelation(%d,%d,%d,%d) = %q, want %q", tt.aStart, tt.aEnd, tt.bStart, tt.bEnd, got, tt.want)
			}
		})
	}
}

func TestCenterNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"用户权限申请（含审批流程）", "用户权限申请"},
		{"用户 权限 申请", "用户权限申请"},
		{"(二)年度积分基准线", "年度积分基准线"},
		{"KILL Session 操作", "killsession操作"},
		{"达梦数据库--统计信息", "达梦数据库统计信息"},
	}
	for _, tt := range tests {
		if got := centerNormalize(tt.in); got != tt.want {
			t.Errorf("centerNormalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCharNGramSim(t *testing.T) {
	if sim := charNGramSim("达梦数据库统计信息更新", "更新达梦数据库统计信息"); sim < centerSimMin {
		t.Errorf("word-order variants score %f, want >= %f", sim, centerSimMin)
	}
	if sim := charNGramSim("服务启动步骤", "服务停止步骤"); sim >= 1.0 {
		t.Errorf("different topics must not score 1.0, got %f", sim)
	}
	if sim := charNGramSim("完全不同的内容", "another topic"); sim > 0.1 {
		t.Errorf("unrelated strings score %f, want ~0", sim)
	}
	if sim := charNGramSim("", "abc"); sim != 0 {
		t.Errorf("empty input scores %f, want 0", sim)
	}
	if sim := charNGramSim("a", "a"); sim != 1 {
		t.Errorf("identical single runes score %f, want 1", sim)
	}
}

// TestCandidatePairs_KnownDuplicatePatterns covers the duplicate shapes
// actually observed in imported documents (the evaluation set's typical
// cases): same range, containment, heading-vs-body, v5-retry twin, and
// cross-segment repetition — plus the adjacent-but-distinct pair that must
// NOT be nominated.
func TestCandidatePairs_KnownDuplicatePatterns(t *testing.T) {
	find := func(pairs []CandidatePair, aID, bID string) *CandidatePair {
		for i := range pairs {
			p := &pairs[i]
			if (p.A.UnitID == aID && p.B.UnitID == bID) || (p.A.UnitID == bID && p.B.UnitID == aID) {
				return p
			}
		}
		return nil
	}
	hasReason := func(p *CandidatePair, reason string) bool {
		for _, r := range p.Reasons {
			if r == reason {
				return true
			}
		}
		return false
	}

	units := []DedupUnit{
		// Pair 1: identical range, near-identical center (v6 unit and its v5
		// retry twin — the handleFailedUnit bypass artifact).
		{UnitID: "u1", Center: "达梦数据库统计信息更新", LineStart: 10, LineEnd: 20, SegmentIndex: 0,
			PointsText: "使用 DBMS_STATS 更新达梦数据库统计信息", SourceText: "更新统计信息 DBMS_STATS.GATHER_TABLE_STATS"},
		{UnitID: "u2", Center: "更新达梦数据库统计信息", LineStart: 10, LineEnd: 20, SegmentIndex: 0,
			PointsText: "通过 DBMS_STATS 完成达梦统计信息更新", SourceText: "更新统计信息 DBMS_STATS.GATHER_TABLE_STATS"},

		// Pair 2: containment — heading got its own unit, body became another.
		{UnitID: "u3", Center: "年度积分基准线", LineStart: 30, LineEnd: 31, SegmentIndex: 1,
			PointsText: "年度积分基准线的定义", SourceText: "(二)年度积分基准线"},
		{UnitID: "u4", Center: "年度积分基准线的计算与调整", LineStart: 30, LineEnd: 45, SegmentIndex: 1,
			PointsText: "年度积分基准线按上年度平均值计算", SourceText: "(二)年度积分基准线 按上年度全员平均积分计算 每年一月调整一次"},

		// Pair 3: cross-segment repetition — same fact restated far away in a
		// different segment. Only text signals can catch it.
		{UnitID: "u5", Center: "差旅住宿费报销限额", LineStart: 60, LineEnd: 65, SegmentIndex: 2,
			PointsText: "一线城市住宿费限额每晚 500 元，超出部分不予报销", SourceText: "住宿费限额：一线城市 500 元/晚，超出不予报销"},
		{UnitID: "u6", Center: "住宿费用报销限额规定", LineStart: 200, LineEnd: 204, SegmentIndex: 5,
			PointsText: "一线城市住宿费限额每晚 500 元，超出部分自理", SourceText: "住宿费报销限额规定：一线城市 500 元每晚，超出部分自理"},

		// Distinct neighbors: adjacent but genuinely different topics
		// (service start vs stop) — must not be nominated.
		{UnitID: "u7", Center: "Oracle 集群服务启动流程", LineStart: 100, LineEnd: 110, SegmentIndex: 3,
			PointsText: "crsctl start cluster 启动集群，随后检查资源状态", SourceText: "crsctl start cluster\nsrvctl start database -d orcl"},
		{UnitID: "u8", Center: "Oracle 数据库补丁检查", LineStart: 112, LineEnd: 122, SegmentIndex: 3,
			PointsText: "opatch lspatches 查看已安装补丁清单", SourceText: "opatch lspatches\nopatch version 检查补丁工具版本"},
	}

	pairs := CandidatePairs(units)

	p12 := find(pairs, "u1", "u2")
	if p12 == nil {
		t.Fatal("same-range pair u1/u2 not nominated")
	}
	if p12.RangeRelation != RangeExact || !hasReason(p12, "range_exact") {
		t.Errorf("u1/u2 relation = %q reasons = %v, want exact", p12.RangeRelation, p12.Reasons)
	}
	if !hasReason(p12, "center_similar") {
		t.Errorf("u1/u2 (word-order centers) reasons = %v, want center_similar (sim=%f)", p12.Reasons, p12.CenterSim)
	}

	p34 := find(pairs, "u3", "u4")
	if p34 == nil {
		t.Fatal("containment pair u3/u4 not nominated")
	}
	if p34.RangeRelation != RangeContains {
		t.Errorf("u3/u4 relation = %q, want contains", p34.RangeRelation)
	}
	if !hasReason(p34, "center_substring") {
		t.Errorf("u3/u4 reasons = %v, want center_substring", p34.Reasons)
	}

	p56 := find(pairs, "u5", "u6")
	if p56 == nil {
		t.Fatal("cross-segment pair u5/u6 not nominated — document-level recall blind spot")
	}
	if p56.RangeRelation != RangeDistant {
		t.Errorf("u5/u6 relation = %q, want distant", p56.RangeRelation)
	}
	if !p56.CrossSegment() {
		t.Error("u5/u6 should report CrossSegment")
	}

	if p78 := find(pairs, "u7", "u8"); p78 != nil {
		t.Errorf("distinct neighbors u7/u8 wrongly nominated: relation=%q reasons=%v centerSim=%f pointSim=%f sourceSim=%f",
			p78.RangeRelation, p78.Reasons, p78.CenterSim, p78.PointSim, p78.SourceSim)
	}
}

func TestCandidatePairs_Empty(t *testing.T) {
	if pairs := CandidatePairs(nil); len(pairs) != 0 {
		t.Errorf("nil input yields %d pairs, want 0", len(pairs))
	}
	if pairs := CandidatePairs([]DedupUnit{{UnitID: "solo", LineStart: 1, LineEnd: 5}}); len(pairs) != 0 {
		t.Errorf("single unit yields %d pairs, want 0", len(pairs))
	}
}
