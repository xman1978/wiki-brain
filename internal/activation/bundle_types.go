package activation

import (
	"database/sql"
	"time"
)

// ActivationBundle（熟路）触发轴状态复用 ActivationLink 的三态连续置信度语义
// （2026-08-13，docs/impl/v1/activation.md 状态机；docs/impl/v1/
// activation-bundle.md 步骤 2/4）——status 是从 ObservedConditions 派生的
// 缓存字段，不是离散状态机。阶段 1 不产生 bundle_success/bundle_failure 事件
// （阶段 2 才有），因此新建的行会停留在 candidate——这是文档明确的预期行为，
// 不是缺口。
const (
	BundleStatusCandidate  = StatusCandidate
	BundleStatusVerified   = StatusVerified
	BundleStatusDeprecated = StatusDeprecated
)

// ActivationBundle 的 learning_results 对象类型（成员轴留给阶段 5）。
const (
	ObjectTypeActivationBundle = "activation_bundle"
)

// BundleMember is one member_point_ids JSON element — a knowledge point's
// own success/failure Beta posterior within this Bundle, independent of the
// Bundle's own trigger-axis ObservedConditions (2026-08-13, docs/impl/v1/
// activation-bundle.md「成员置信度：Bundle 独有的第二根轴」). Replaces the
// prior static MemberPointIDs（核心）/FringePointIDs（路肩）two-array split
// — "core" vs "fringe" is now derived by applying ConditionTier to
// (SuccessCount, FailureCount), not a label written once and left stale.
type BundleMember struct {
	PointID      string    `json:"point_id"`
	SuccessCount int       `json:"success_count"`
	FailureCount int       `json:"failure_count"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

type ActivationBundle struct {
	BundleID            string
	ClusterFingerprint  string
	RepresentativeTerms string
	ObservedConditions  []ObservedCondition
	Members             []BundleMember
	Status              string
	AdoptCount          int
	FailCount           int
	LastUsedAt          sql.NullTime
	CreatedFrom         string
	StatusChangedAt     sql.NullTime
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// MemberPointIDs returns every member's point_id regardless of tier — the
// full membership set (former core+fringe union).
func (b ActivationBundle) MemberPointIDs() []string {
	out := make([]string, 0, len(b.Members))
	for _, m := range b.Members {
		out = append(out, m.PointID)
	}
	return out
}

// CoreMemberPointIDs returns the point_ids of members whose mean(member)
// tier is self_graded or trusted — the tier-derived replacement for the old
// static "core" array (docs/impl/v1/activation-bundle.md「组装时的用法」).
func (b ActivationBundle) CoreMemberPointIDs(cfg ConfidenceConfig) []string {
	var out []string
	for _, m := range b.Members {
		tier, _ := conditionTier(memberAsCondition(m), cfg)
		if tier == TierSelfGraded || tier == TierTrusted {
			out = append(out, m.PointID)
		}
	}
	return out
}

// memberAsCondition adapts a BundleMember's two counts into the minimal
// ObservedCondition shape conditionTier needs — plumbing only, does not
// duplicate the Beta math itself (docs/impl/v1/activation-bundle.md 步骤 1's
// "核心/路肩判定读时用 conditionTier 对 BundleMember 求值").
func memberAsCondition(m BundleMember) ObservedCondition {
	return ObservedCondition{SuccessCount: m.SuccessCount, FailureCount: m.FailureCount}
}

// BundleMatch is a single Bundle Match() result — same MatchedBy vocabulary,
// and (2026-08-13) same confidence-tier fields, as LinkMatch
// (docs/impl/v1/activation-bundle.md 步骤 2, 复用阶段 1 的置信度分档核心).
type BundleMatch struct {
	Bundle       ActivationBundle
	Score        float64
	MatchedBy    string
	Tier         Tier
	Mean         float64
	AuditSampled bool
	Subject      string
	Intent       string
	Audience     string
	Constraint   string
}
