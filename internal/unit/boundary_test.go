package unit

import "testing"

func TestLocateUnitBounds_Exact(t *testing.T) {
	mdLines := []string{
		"# 配置 SSH 无密码登录通道",
		"生成 RSA 密钥对并交换公钥",
		"# 启动停止 RAC 集群",
		"停止数据库需执行 srvctl stop database",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	lineStart, lineEnd, nextCursor, ok := LocateUnitBounds(mdLines, seg,
		"# 配置 SSH 无密码登录通道", "生成 RSA 密钥对并交换公钥", seg.LineStart)
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

	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, "正文内容", "正文内容", seg.LineStart)
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
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, "步骤一： 先做 A", "步骤二：做 B", seg.LineStart)
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

	l1Start, l1End, next, ok := LocateUnitBounds(mdLines, seg, "# su – oracle", "srvctl stop database", seg.LineStart)
	if !ok || l1Start != 1 || l1End != 2 {
		t.Fatalf("unit1: got %d-%d ok=%v, want 1-2", l1Start, l1End, ok)
	}

	l2Start, l2End, _, ok := LocateUnitBounds(mdLines, seg, "# su – oracle", "srvctl start database", next)
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
	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg, "# 标题一", "内容一", 3)
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

	_, _, _, ok := LocateUnitBounds(mdLines, seg, "a sentence that never appears here", "also missing", seg.LineStart)
	if ok {
		t.Error("expected hallucinated anchor to not match")
	}
}

func TestLocateUnitBounds_SearchStaysWithinSegment(t *testing.T) {
	// The segment is lines 3-4 only; "标题一" living at line 1 (outside the
	// segment) must not be found even though its text exists in mdLines.
	mdLines := []string{"# 标题一", "内容一", "# 标题二", "内容二"}
	seg := Segment{LineStart: 3, LineEnd: 4}

	_, _, _, ok := LocateUnitBounds(mdLines, seg, "标题一", "内容一", seg.LineStart)
	if ok {
		t.Error("expected search to be confined to the segment's own line range")
	}
}
