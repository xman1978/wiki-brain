package unit

import "testing"

// unverified is passed as reportedStart/reportedEnd in tests that only care
// about the v4 scan fallback (no verifiable model-reported line number),
// exercising the same path as before v5 added report verification.
const unverified = 0

func TestLocateUnitBounds_Exact(t *testing.T) {
	mdLines := []string{
		"# 配置 SSH 无密码登录通道",
		"生成 RSA 密钥对并交换公钥",
		"# 启动停止 RAC 集群",
		"停止数据库需执行 srvctl stop database",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	lineStart, lineEnd, nextCursor, ok := LocateUnitBounds(mdLines, seg,
		unverified, "# 配置 SSH 无密码登录通道", unverified, "生成 RSA 密钥对并交换公钥", seg.LineStart)
	if !ok {
		t.Fatal("expected match")
	}
	if lineStart != 1 || lineEnd != 2 {
		t.Fatalf("got %d-%d, want 1-2", lineStart, lineEnd)
	}
	if nextCursor != 3 {
		t.Fatalf("nextCursor = %d, want 3", nextCursor)
	}
}

func TestLocateUnitBounds_SingleLine(t *testing.T) {
	mdLines := []string{"# 标题", "正文内容", "结尾"}
	seg := Segment{LineStart: 1, LineEnd: 3}

	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, unverified, "正文内容", unverified, "正文内容", seg.LineStart)
	if !ok {
		t.Fatal("expected match")
	}
	if lineStart != 2 || lineEnd != 2 {
		t.Fatalf("got %d-%d, want 2-2", lineStart, lineEnd)
	}
}

func TestLocateUnitBounds_FuzzyWhitespace(t *testing.T) {
	mdLines := []string{"步骤一：  先做 A", "步骤二：做 B"}
	seg := Segment{LineStart: 1, LineEnd: 2}

	// Model collapsed the irregular spacing when copying the anchor.
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, unverified, "步骤一： 先做 A", unverified, "步骤二：做 B", seg.LineStart)
	if !ok {
		t.Fatal("expected fuzzy match to succeed")
	}
	if lineStart != 1 || lineEnd != 2 {
		t.Fatalf("got %d-%d, want 1-2", lineStart, lineEnd)
	}
}

func TestLocateUnitBounds_CursorDisambiguatesDuplicateLines(t *testing.T) {
	mdLines := []string{
		"# su – oracle",
		"srvctl stop database -d oarac",
		"# su – oracle",
		"srvctl start database -d oarac",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	l1Start, l1End, next, ok := LocateUnitBounds(mdLines, seg, unverified, "# su – oracle", unverified, "srvctl stop database", seg.LineStart)
	if !ok || l1Start != 1 || l1End != 2 {
		t.Fatalf("unit1: got %d-%d ok=%v, want 1-2", l1Start, l1End, ok)
	}

	l2Start, l2End, _, ok := LocateUnitBounds(mdLines, seg, unverified, "# su – oracle", unverified, "srvctl start database", next)
	if !ok {
		t.Fatal("expected second duplicate-anchor unit to resolve to the second occurrence")
	}
	if l2Start != 3 || l2End != 4 {
		t.Fatalf("unit2: got %d-%d, want 3-4 (cursor should have skipped the first '# su – oracle')", l2Start, l2End)
	}
}

func TestLocateUnitBounds_OutOfOrderFallsBackToSegmentStart(t *testing.T) {
	mdLines := []string{"# 标题一", "内容一", "# 标题二", "内容二"}
	seg := Segment{LineStart: 1, LineEnd: 4}

	// cursor is past unit1's lines because a later unit was resolved first,
	// but the model lists unit1 (lines 1-2) after unit2 in its output.
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, unverified, "# 标题一", unverified, "内容一", 3)
	if !ok {
		t.Fatal("expected fallback search from seg.LineStart to find it")
	}
	if lineStart != 1 || lineEnd != 2 {
		t.Fatalf("got %d-%d, want 1-2", lineStart, lineEnd)
	}
}

func TestLocateUnitBounds_AnchorNotFound(t *testing.T) {
	mdLines := []string{"the quick brown fox", "jumps over the lazy dog"}
	seg := Segment{LineStart: 1, LineEnd: 2}

	_, _, _, ok := LocateUnitBounds(mdLines, seg, unverified, "a sentence that never appears here", unverified, "also missing", seg.LineStart)
	if ok {
		t.Error("expected hallucinated anchor to not match")
	}
}

func TestLocateUnitBounds_SearchStaysWithinSegment(t *testing.T) {
	// The segment is lines 3-4 only; "标题一" living at line 1 (outside the
	// segment) must not be found even though its text exists in mdLines.
	mdLines := []string{"# 标题一", "内容一", "# 标题二", "内容二"}
	seg := Segment{LineStart: 3, LineEnd: 4}

	_, _, _, ok := LocateUnitBounds(mdLines, seg, unverified, "标题一", unverified, "内容一", seg.LineStart)
	if ok {
		t.Error("expected search to be confined to the segment's own line range")
	}
}

// TestLocateUnitBounds_ReportedLineTrustedWhenVerified is the core v5
// regression test: a verified (line_start, first_line_anchor) pair is
// trusted directly instead of falling through to the blind scan — proven
// here by having cursor point at the *wrong* occurrence of a duplicated
// line, something the v4-only scan could never disambiguate on its own.
func TestLocateUnitBounds_ReportedLineTrustedWhenVerified(t *testing.T) {
	mdLines := []string{
		"# su – oracle",
		"srvctl stop database -d oarac",
		"# su – oracle",
		"srvctl start database -d oarac",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	// cursor is deliberately wrong (points past both occurrences); only the
	// verified report should be able to resolve this correctly.
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, 3, "# su – oracle", 4, "srvctl start database -d oarac", 5)
	if !ok {
		t.Fatal("expected verified report to resolve despite unhelpful cursor")
	}
	if lineStart != 3 || lineEnd != 4 {
		t.Fatalf("got %d-%d, want 3-4 (the reported occurrence, not the first)", lineStart, lineEnd)
	}
}

// TestLocateUnitBounds_ReportedLineWrongFallsBackToScan covers a model that
// miscounted the [N] marker: the reported number doesn't correspond to the
// anchor text, so it must not be trusted (the v2/v3 failure mode) — it
// should fall back to the same content scan v4 always used, and still
// resolve correctly if the anchor text is genuinely present elsewhere.
func TestLocateUnitBounds_ReportedLineWrongFallsBackToScan(t *testing.T) {
	mdLines := []string{"# 标题一", "内容一", "# 标题二", "内容二"}
	seg := Segment{LineStart: 1, LineEnd: 4}

	// reportedStart=3 is wrong (that's "# 标题二", not "# 标题一").
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, 3, "# 标题一", 2, "内容一", seg.LineStart)
	if !ok {
		t.Fatal("expected fallback scan to still resolve a genuinely-present anchor")
	}
	if lineStart != 1 || lineEnd != 2 {
		t.Fatalf("got %d-%d, want 1-2 (scan-resolved, reported number ignored)", lineStart, lineEnd)
	}
}

// TestLocateUnitBounds_ReportedLineOutOfSegmentFallsBack covers a reported
// number that lands outside the segment's own bounds (e.g. the model
// confused this segment's local text with a neighboring one) — it must not
// be trusted even though it's a well-formed integer.
func TestLocateUnitBounds_ReportedLineOutOfSegmentFallsBack(t *testing.T) {
	mdLines := []string{"# 标题一", "内容一", "# 标题二", "内容二"}
	seg := Segment{LineStart: 3, LineEnd: 4}

	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, 1, "# 标题二", 4, "内容二", seg.LineStart)
	if !ok {
		t.Fatal("expected fallback scan to resolve despite out-of-segment report")
	}
	if lineStart != 3 || lineEnd != 4 {
		t.Fatalf("got %d-%d, want 3-4", lineStart, lineEnd)
	}
}

// TestLocateUnitBounds_ReportedLineContentMismatchFallsBack covers the
// motivating real-world failure: the model's anchor blends a heading line
// with the following body line (as if they were one continuous sentence),
// so the reported line_start's actual content doesn't contain the anchor
// text. Verification must reject this and fall back to scanning each line
// independently — the anchor can still resolve as long as it's genuinely
// findable as a single physical line somewhere in the segment.
func TestLocateUnitBounds_ReportedLineContentMismatchFallsBack(t *testing.T) {
	mdLines := []string{
		"二、其他日常费用报销期限",
		"",
		"为确保财务数据的时效性，个人因公发生的办公费用……",
	}
	seg := Segment{LineStart: 1, LineEnd: 3}

	// Model reports line_start=1 but writes an anchor that blends line 1's
	// heading with line 3's opening words — line 1 alone doesn't contain it,
	// and no single physical line contains it either, so both the reported
	// number and the scan fallback must reject it.
	_, _, _, ok := LocateUnitBounds(mdLines, seg, 1, "二、其他日常费用报销期限为确保财务数据的时效性", 3, "为确保财务数据的时效性，个人因公发生的办公费用……", seg.LineStart)
	if ok {
		t.Error("expected a genuinely cross-physical-line anchor to still fail to locate — the Go-side fix cannot invent a line that isn't there; this case is fixed by the prompt asking the model to quote a single physical line, not by boundary.go")
	}
}
