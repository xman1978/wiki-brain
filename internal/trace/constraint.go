package trace

import (
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// 约束一致性判定（docs/impl/v1/trace.md 步骤 1 扩展，2026-07-21）：
// confident 分级要求"被引用的直接证据"与问题约束不冲突。问题约束（Session
// 解析的 constraint）指向的实体与证据所属 KU 的预计算语义（source_theme/
// content_theme/object/scope）指向不同实体时，该引用不计入 direct_point_ids
// ——库里没有对应资料时，正确的系统行为是知识盲区，而不是拿相邻领域的材料
// confident 作答并固化成学习信号。纯确定性词项规则，不调用 LLM。

// splitConstraintItems splits a session-parsed constraint_text into
// independent items（如 "达梦, Windows环境" → ["达梦","Windows环境"]），
// 每项单独参与冲突判定——一项匹配证据、另一项正交时不应误伤。
func splitConstraintItems(constraintText string) []string {
	items := strings.FieldsFunc(constraintText, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；':
			return true
		}
		return false
	})
	var out []string
	for _, it := range items {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}

// constraintConflicts reports whether one constraint item conflicts with the
// evidence-side term set: the item shares at least one term with the evidence
// (they talk about the same dimension, e.g. both mention 数据库) yet also
// carries a term the evidence lacks (a different entity on that dimension,
// e.g. 神通 vs 达梦 material). Items sharing no term are orthogonal
// constraints (生产环境、Windows环境), never conflicts — the rule only fires
// on "same dimension, different entity", which keeps false positives to
// qualifiers glued onto a shared dimension word.
func constraintConflicts(item string, evidenceTerms map[string]struct{}) bool {
	shared, missing := false, false
	for term := range text.TermSet(item) {
		if _, ok := evidenceTerms[term]; ok {
			shared = true
		} else {
			missing = true
		}
	}
	return shared && missing
}

// pointConflictsWithConstraint reports whether any constraint item conflicts
// with the point's unit-semantics term set. Points whose unit has no
// precomputed semantics (nil set) are never dropped — no basis to judge.
func pointConflictsWithConstraint(items []string, evidenceTerms map[string]struct{}) bool {
	if len(evidenceTerms) == 0 {
		return false
	}
	for _, item := range items {
		if constraintConflicts(item, evidenceTerms) {
			return true
		}
	}
	return false
}
