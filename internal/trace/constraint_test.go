package trace

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func TestSplitConstraintItems(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"达梦, Windows环境", 2},
		{"达梦数据库", 1},
		{"达梦、神通；windows", 3},
		{"", 0},
		{" , ", 0},
	}
	for _, c := range cases {
		if got := splitConstraintItems(c.in); len(got) != c.want {
			t.Errorf("splitConstraintItems(%q) = %v, want %d items", c.in, got, c.want)
		}
	}
}

func TestConstraintConflicts(t *testing.T) {
	evidence := text.TermSet("达梦数据库优化 数据库会话监控 达梦数据库")

	// 同维度不同实体（共享"数据库"、多出"神通"）→ 冲突
	if !constraintConflicts("神通数据库", evidence) {
		t.Error("expected 神通数据库 to conflict with 达梦 evidence terms")
	}
	// 完全被证据词集包含 → 不冲突
	if constraintConflicts("达梦数据库", evidence) {
		t.Error("expected 达梦数据库 not to conflict")
	}
	// 正交维度（无共享词）→ 不冲突
	if constraintConflicts("生产环境", evidence) {
		t.Error("expected orthogonal 生产环境 not to conflict")
	}
	// 证据侧无语义信息 → 不冲突
	if pointConflictsWithConstraint([]string{"神通数据库"}, nil) {
		t.Error("expected no conflict when unit has no semantics row")
	}
	// 多项约束：一项包含、一项正交 → 整体不冲突
	if pointConflictsWithConstraint([]string{"达梦", "Windows环境"}, evidence) {
		t.Error("expected 达梦+Windows环境 not to conflict")
	}
}

func insertTestKPContent(t *testing.T, db *sql.DB, pointID, content string) {
	t.Helper()
	db.Exec(`INSERT OR IGNORE INTO sources (source_id, title, format, file_name, original_path, markdown_path, status) VALUES ('s-test', 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'completed')`)
	db.Exec(`INSERT OR IGNORE INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version) VALUES ('u-test', 's-test', 'test', 1, 10, 'completed', 'v1')`)
	_, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type) VALUES (?, 'u-test', 's-test', ?, 'fact')
		ON CONFLICT(point_id) DO UPDATE SET content = excluded.content`, pointID, content)
	if err != nil {
		t.Fatalf("insert test kp content: %v", err)
	}
}

func mismatchAnswerResult(constraint string) *answer.AnswerResult {
	return &answer.AnswerResult{
		AnswerID:  "a-001",
		Question:  "数据库怎么查会话？",
		Content:   "……",
		Citations: []string{"f1"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			Constraint: constraint,
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1", UnitID: "u-test"},
			},
		},
	}
}

// 问题约束指向不同实体（神通）而该 KP 自己的内容属于达梦 → 引用被剔除，
// 分级降为 partial，不产生 confident 共现信号（2026-08-16 起判据换成 KP
// 自己的 content，不再是 unit 级预算摘要 —— 见 resolveDirectEvidence）。
func TestProcessTrace_ConstraintMismatch_DowngradesToPartial(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-001")
	insertTestKPContent(t, db, "p1", "达梦数据库优化 数据库会话监控 达梦数据库")

	svc.ProcessTrace(mismatchAnswerResult("神通数据库"))

	traces, _ := store.ListTraces(QualityPartial, "", "", 20, 0)
	if len(traces) != 1 {
		t.Fatalf("expected 1 partial trace after constraint gate, got %d", len(traces))
	}
	full, _ := store.GetTrace(traces[0].TraceID)
	if len(full.DirectPointIDs) != 0 {
		t.Errorf("expected empty direct_point_ids, got %v", full.DirectPointIDs)
	}
	coocs, _ := store.ListCooccurrence("p1", 0, 50)
	if len(coocs) != 0 {
		t.Errorf("expected no cooccurrence rows for the dropped point, got %v", coocs)
	}
}

// 约束与 KP 内容一致（达梦数据库）或正交（生产环境）时分级不受影响。
func TestProcessTrace_ConstraintCompatible_StaysConfident(t *testing.T) {
	for _, constraint := range []string{"达梦数据库", "生产环境", "达梦数据库, Windows环境"} {
		t.Run(constraint, func(t *testing.T) {
			svc, store, db := setupService(t)
			insertTestAnswer(t, db, "a-001")
			insertTestKPContent(t, db, "p1", "达梦数据库优化 数据库会话监控 达梦数据库")

			svc.ProcessTrace(mismatchAnswerResult(constraint))

			traces, _ := store.ListTraces(QualityConfident, "", "", 20, 0)
			if len(traces) != 1 {
				t.Fatalf("constraint %q: expected 1 confident trace, got %d", constraint, len(traces))
			}
			full, _ := store.GetTrace(traces[0].TraceID)
			if len(full.DirectPointIDs) != 1 || full.DirectPointIDs[0] != "p1" {
				t.Errorf("constraint %q: expected direct_point_ids=[p1], got %v", constraint, full.DirectPointIDs)
			}
		})
	}
}

// KP 内容与约束无任何共享词（正交，而非"同维度不同实体"）时不算冲突，
// 分级照旧 —— 覆盖 insertTestKP 默认写入的泛化 content（'test'）这种
// 现实中"该 unit 没有可比对语义"的等价情形。
func TestProcessTrace_ConstraintGate_OrthogonalContentSkips(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-001")
	insertTestKP(t, db, "p1")

	svc.ProcessTrace(mismatchAnswerResult("神通数据库"))

	traces, _ := store.ListTraces(QualityConfident, "", "", 20, 0)
	if len(traces) != 1 {
		t.Fatalf("expected 1 confident trace when content has no overlapping terms, got %d", len(traces))
	}
}
