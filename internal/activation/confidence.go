package activation

// Tier is a condition's service tier, derived (never stored) from its
// success/failure Beta posterior (docs/impl/v1/activation.md 状态机「服务分档」).
type Tier string

const (
	TierExploring  Tier = "exploring"
	TierSelfGraded Tier = "self_graded"
	TierTrusted    Tier = "trusted"
)

// ConfidenceConfig holds the five retrieval.* knobs that drive tiering and
// exploration/audit sampling (docs/impl/v1/activation.md 配置项). Shared by
// Matcher and BundleMatcher via SetConfidenceConfig, and by
// Service.deriveAndPersistStatus.
type ConfidenceConfig struct {
	ServingConfidenceMin  float64
	AuditSampleMin        int
	ExploreRateLow        float64
	ExploreRateSelfGraded float64
	ExploreRateTrusted    float64
}

// conditionMean is the Beta(success+1, failure+1) posterior mean — standard
// Laplace smoothing. A brand-new condition (0/0) starts at 0.5.
func conditionMean(successCount, failureCount int) float64 {
	return float64(successCount+1) / float64(successCount+failureCount+2)
}

// ConditionMean is the exported wrapper around conditionMean — reused by
// Study's create-threshold gate and convergence剪枝/report aggregation
// (docs/impl/v1/study.md 步骤 1/3/7) so both packages compute mean_pre/mean
// with the exact same formula, not two independently-written copies.
func ConditionMean(successCount, failureCount int) float64 {
	return conditionMean(successCount, failureCount)
}

// conditionTier implements docs/impl/v1/activation.md「服务分档」exactly:
//
//	mean < serving_confidence_min                                → exploring
//	else audited_n >= audit_sample_min && audited_mean >= serving_confidence_min → trusted
//	else                                                          → self_graded
//
// ConditionTier is the exported wrapper around conditionTier — Study's
// convergence-report aggregation (docs/impl/v1/study.md 步骤 7) needs the
// same tier boundaries Match()/deriveStatus use, without reimplementing them.
func ConditionTier(cond ObservedCondition, cfg ConfidenceConfig) (Tier, float64) {
	return conditionTier(cond, cfg)
}

func conditionTier(cond ObservedCondition, cfg ConfidenceConfig) (Tier, float64) {
	mean := conditionMean(cond.SuccessCount, cond.FailureCount)
	if mean < cfg.ServingConfidenceMin {
		return TierExploring, mean
	}
	auditedN := cond.AuditedSuccessCount + cond.AuditedFailureCount
	if auditedN >= cfg.AuditSampleMin {
		auditedMean := conditionMean(cond.AuditedSuccessCount, cond.AuditedFailureCount)
		if auditedMean >= cfg.ServingConfidenceMin {
			return TierTrusted, mean
		}
	}
	return TierSelfGraded, mean
}

// deriveStatus implements docs/impl/v1/activation.md「与旧状态机的映射」:
// verified iff at least one condition's tier is self_graded or trusted;
// candidate otherwise (including the empty-conditions case — the default
// landing point when there's no more specific information). deprecated is
// NOT derived here — it depends on the target KP's lifecycle, an external
// fact this function has no access to; callers (Service.deriveAndPersistStatus)
// check that separately and it takes priority over whatever this returns.
func deriveStatus(conds []ObservedCondition, cfg ConfidenceConfig) string {
	for _, c := range conds {
		tier, _ := conditionTier(c, cfg)
		if tier == TierSelfGraded || tier == TierTrusted {
			return StatusVerified
		}
	}
	return StatusCandidate
}
