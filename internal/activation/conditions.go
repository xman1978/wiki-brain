package activation

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// ObservedCondition is one historically observed Session quadruple for a link
// (docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md).
// Match requires all four fields within the same group; groups OR across the list.
//
// 2026-08-13 起（docs/design/activation-convergence.md, docs/impl/v1/activation.md
// 状态机）：HitCount 更名 SuccessCount，新增 FailureCount/AuditedSuccessCount/
// AuditedFailureCount 承接连续置信度（Beta 后验）的证据计数；
// KnownQuestionTerms 从 ActivationLink 表级列下沉为条件级字段，是字面问题
// 捷径（见 matcher.go）反查"归属条件"的依据。
type ObservedCondition struct {
	Subject             string    `json:"subject"`
	Intent              string    `json:"intent"`
	Audience            string    `json:"audience"`
	Constraint          string    `json:"constraint"`
	QuestionTerms       string    `json:"question_terms,omitempty"`
	FirstSeenAt         time.Time `json:"first_seen_at"`
	LastSeenAt          time.Time `json:"last_seen_at"`
	SuccessCount        int       `json:"success_count"`
	FailureCount        int       `json:"failure_count"`
	AuditedSuccessCount int       `json:"audited_success_count"`
	AuditedFailureCount int       `json:"audited_failure_count"`
	// KnownQuestionTerms accumulates every literal question's normalized term
	// set that has ever routed to this specific condition (2026-08-13 下沉自
	// ActivationLink.KnownQuestionTerms，见 matcher.go 字面问题捷径).
	KnownQuestionTerms []string `json:"known_question_terms,omitempty"`
}

// NormalizeObservedCondition builds a dedupe-ready tuple from raw Session fields.
func NormalizeObservedCondition(subject, intent, audience, constraint, questionTerms string, now time.Time) ObservedCondition {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c := ObservedCondition{
		Subject:       text.Normalize(subject),
		Intent:        text.Terms(text.Normalize(intent)),
		Audience:      text.NormalizeCompact(audience),
		Constraint:    text.Terms(text.Normalize(constraint)),
		QuestionTerms: questionTerms,
		FirstSeenAt:   now,
		LastSeenAt:    now,
		SuccessCount:  1,
	}
	if questionTerms != "" {
		c.KnownQuestionTerms = []string{questionTerms}
	}
	return c
}

func conditionKey(c ObservedCondition) string {
	return c.Subject + "\x1f" + c.Intent + "\x1f" + c.Audience + "\x1f" + c.Constraint
}

// mergeKnownQuestionTerms unions two term sets, dedups, and caps at
// maxKnownQuestionTerms (alphabetical trim — the set carries no per-entry
// recency, same convention as the pre-2026-08-13 link-level column).
func mergeKnownQuestionTerms(existing []string, add string) []string {
	set := make(map[string]struct{}, len(existing)+1)
	for _, q := range existing {
		if q != "" {
			set[q] = struct{}{}
		}
	}
	if add != "" {
		set[add] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	sort.Strings(out)
	if len(out) > maxKnownQuestionTerms {
		out = out[:maxKnownQuestionTerms]
	}
	return out
}

// MergeObservedConditions unions by quadruple key, bumps success_count/
// last_seen, folds QuestionTerms into KnownQuestionTerms, then trims to max
// (oldest last_seen_at dropped first). max<=0 means 50.
func MergeObservedConditions(existing []ObservedCondition, add ObservedCondition, max int) []ObservedCondition {
	if max <= 0 {
		max = 50
	}
	byKey := make(map[string]ObservedCondition, len(existing)+1)
	order := make([]string, 0, len(existing)+1)
	for _, c := range existing {
		k := conditionKey(c)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = c
	}
	k := conditionKey(add)
	if prev, ok := byKey[k]; ok {
		prev.SuccessCount++
		if add.LastSeenAt.After(prev.LastSeenAt) {
			prev.LastSeenAt = add.LastSeenAt
		}
		if add.QuestionTerms != "" {
			prev.QuestionTerms = add.QuestionTerms
		}
		prev.KnownQuestionTerms = mergeKnownQuestionTerms(prev.KnownQuestionTerms, add.QuestionTerms)
		byKey[k] = prev
	} else {
		add.KnownQuestionTerms = mergeKnownQuestionTerms(add.KnownQuestionTerms, add.QuestionTerms)
		byKey[k] = add
		order = append(order, k)
	}
	out := make([]ObservedCondition, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return trimObservedConditions(out, max)
}

// ReplaceObservedConditionsList dedupes a full rebuild list and applies max.
func ReplaceObservedConditionsList(conds []ObservedCondition, max int) []ObservedCondition {
	if max <= 0 {
		max = 50
	}
	if len(conds) == 0 {
		return []ObservedCondition{}
	}
	var out []ObservedCondition
	for _, c := range conds {
		if c.SuccessCount <= 0 {
			c.SuccessCount = 1
		}
		if c.FirstSeenAt.IsZero() {
			c.FirstSeenAt = c.LastSeenAt
		}
		if c.LastSeenAt.IsZero() {
			c.LastSeenAt = c.FirstSeenAt
		}
		out = MergeObservedConditions(out, c, max)
	}
	return out
}

func trimObservedConditions(conds []ObservedCondition, max int) []ObservedCondition {
	if len(conds) <= max {
		return conds
	}
	sorted := append([]ObservedCondition(nil), conds...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].LastSeenAt.Before(sorted[j].LastSeenAt)
	})
	return sorted[len(sorted)-max:]
}

// ProjectLegacyFields fills deprecated-for-match columns from the newest group.
func ProjectLegacyFields(conds []ObservedCondition) (subjectTerms string, intent, audience, constraint []string) {
	if len(conds) == 0 {
		return "", []string{}, []string{}, []string{}
	}
	newest := conds[0]
	for _, c := range conds[1:] {
		if c.LastSeenAt.After(newest.LastSeenAt) {
			newest = c
		}
	}
	subjectTerms = text.Terms(newest.Subject)
	return subjectTerms, []string{newest.Intent}, []string{newest.Audience}, []string{newest.Constraint}
}

// EffectiveConditions returns ObservedConditions, or synthesizes one group from
// legacy LinkCondition fields (tests / transitional CreateLink callers).
func (c LinkCondition) EffectiveConditions() []ObservedCondition {
	if len(c.ObservedConditions) > 0 {
		return c.ObservedConditions
	}
	if c.SubjectTerms == "" && len(c.IntentTerms) == 0 && len(c.Audience) == 0 && len(c.ConstraintTerms) == 0 {
		return nil
	}
	intent, audience, constraint := "", "", ""
	if len(c.IntentTerms) > 0 {
		intent = text.Terms(text.Normalize(c.IntentTerms[0]))
	}
	if len(c.Audience) > 0 {
		audience = text.NormalizeCompact(c.Audience[0])
	}
	if len(c.ConstraintTerms) > 0 {
		constraint = text.Terms(text.Normalize(c.ConstraintTerms[0]))
	}
	// SubjectTerms is already a terms string; store as-is for Contains matching.
	now := time.Now().UTC()
	return []ObservedCondition{{
		Subject:      c.SubjectTerms,
		Intent:       intent,
		Audience:     audience,
		Constraint:   constraint,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		SuccessCount: 1,
	}}
}

// ConditionsEqual reports whether two condition lists match on quadruple keys
// (ignores hit_count / timestamps).
func ConditionsEqual(a, b []ObservedCondition) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, c := range a {
		set[conditionKey(c)] = struct{}{}
	}
	for _, c := range b {
		if _, ok := set[conditionKey(c)]; !ok {
			return false
		}
	}
	return true
}

func encodeObservedConditions(conds []ObservedCondition) (string, error) {
	if conds == nil {
		conds = []ObservedCondition{}
	}
	b, err := json.Marshal(conds)
	if err != nil {
		return "", fmt.Errorf("activation: encode observed_conditions: %w", err)
	}
	return string(b), nil
}

func decodeObservedConditions(raw string) ([]ObservedCondition, error) {
	if raw == "" {
		return []ObservedCondition{}, nil
	}
	var conds []ObservedCondition
	if err := json.Unmarshal([]byte(raw), &conds); err != nil {
		return nil, fmt.Errorf("activation: decode observed_conditions: %w", err)
	}
	if conds == nil {
		conds = []ObservedCondition{}
	}
	return conds, nil
}

// HasNonEmptyGate reports whether any condition group carries a non-empty
// audience or constraint (used for empty-list fallback Match gating).
func HasNonEmptyGate(conds []ObservedCondition) bool {
	for _, c := range conds {
		if c.Audience != "" || c.Constraint != "" {
			return true
		}
	}
	return false
}
