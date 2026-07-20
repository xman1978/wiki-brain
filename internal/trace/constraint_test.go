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

func insertTestUnitSemantics(t *testing.T, db *sql.DB, unitID, sourceTheme, contentTheme, object, scope string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO unit_rerank_semantics (unit_id, source_theme, content_theme, intent, object, scope, prompt_version)
		VALUES (?, ?, ?, '说明', ?, ?, 'v12')`, unitID, sourceTheme, contentTheme, object, scope)
	if err != nil {
		t.Fatalf("insert unit semantics: %v", err)
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

// 问题约束指向不同实体（神通）而证据语义属于达梦 → 引用被剔除，分级降为
// partial，不产生 confident 共现信号。
func TestProcessTrace_ConstraintMismatch_DowngradesToPartial(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-001")
	insertTestKP(t, db, "p1")
	insertTestUnitSemantics(t, db, "u-test", "达梦数据库优化", "数据库会话监控", "数据库会话", "达梦数据库")

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

// 约束与证据一致（达梦数据库）或正交（生产环境）时分级不受影响。
func TestProcessTrace_ConstraintCompatible_StaysConfident(t *testing.T) {
	for _, constraint := range []string{"达梦数据库", "生产环境", "达梦数据库, Windows环境"} {
		t.Run(constraint, func(t *testing.T) {
			svc, store, db := setupService(t)
			insertTestAnswer(t, db, "a-001")
			insertTestKP(t, db, "p1")
			insertTestUnitSemantics(t, db, "u-test", "达梦数据库优化", "数据库会话监控", "数据库会话", "达梦数据库")

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

// 单元缺少预计算语义时守门不启用，分级照旧。
func TestProcessTrace_ConstraintGate_SkipsWithoutSemantics(t *testing.T) {
	svc, store, db := setupService(t)
	insertTestAnswer(t, db, "a-001")
	insertTestKP(t, db, "p1")

	svc.ProcessTrace(mismatchAnswerResult("神通数据库"))

	traces, _ := store.ListTraces(QualityConfident, "", "", 20, 0)
	if len(traces) != 1 {
		t.Fatalf("expected 1 confident trace when semantics missing, got %d", len(traces))
	}
}
