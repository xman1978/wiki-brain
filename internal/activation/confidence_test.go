package activation

import (
	"context"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

func TestConditionMean(t *testing.T) {
	cases := []struct {
		success, failure int
		want             float64
	}{
		{0, 0, 0.5},
		{1, 0, 2.0 / 3.0},
		{0, 1, 1.0 / 3.0},
		{49, 0, 50.0 / 51.0},
	}
	for _, c := range cases {
		got := conditionMean(c.success, c.failure)
		if got != c.want {
			t.Errorf("conditionMean(%d,%d) = %v, want %v", c.success, c.failure, got, c.want)
		}
	}
}

func TestConditionTier_Boundaries(t *testing.T) {
	cfg := ConfidenceConfig{ServingConfidenceMin: 0.7, AuditSampleMin: 5}

	// Below serving threshold -> exploring.
	exploring := ObservedCondition{SuccessCount: 1, FailureCount: 2} // mean = 2/5 = 0.4
	if tier, _ := conditionTier(exploring, cfg); tier != TierExploring {
		t.Errorf("tier = %q, want exploring", tier)
	}

	// Above serving threshold, no audit sample -> self_graded.
	selfGraded := ObservedCondition{SuccessCount: 10, FailureCount: 0} // mean = 11/12 ≈ 0.917
	if tier, _ := conditionTier(selfGraded, cfg); tier != TierSelfGraded {
		t.Errorf("tier = %q, want self_graded", tier)
	}

	// Above serving threshold, audited sample below audit_sample_min -> still self_graded.
	underSampled := ObservedCondition{SuccessCount: 10, FailureCount: 0, AuditedSuccessCount: 2, AuditedFailureCount: 0}
	if tier, _ := conditionTier(underSampled, cfg); tier != TierSelfGraded {
		t.Errorf("tier = %q, want self_graded (audited_n=2 < audit_sample_min=5)", tier)
	}

	// Above serving threshold, enough audited samples, audited mean also above
	// threshold -> trusted.
	trusted := ObservedCondition{SuccessCount: 10, FailureCount: 0, AuditedSuccessCount: 5, AuditedFailureCount: 0}
	if tier, _ := conditionTier(trusted, cfg); tier != TierTrusted {
		t.Errorf("tier = %q, want trusted", tier)
	}

	// Enough audited samples but audited mean below threshold -> falls back to
	// self_graded, not trusted (audited evidence itself isn't confident).
	auditedButUnconvincing := ObservedCondition{SuccessCount: 10, FailureCount: 0, AuditedSuccessCount: 1, AuditedFailureCount: 5}
	if tier, _ := conditionTier(auditedButUnconvincing, cfg); tier != TierSelfGraded {
		t.Errorf("tier = %q, want self_graded (audited_mean below threshold)", tier)
	}
}

func TestDeriveStatus(t *testing.T) {
	cfg := ConfidenceConfig{ServingConfidenceMin: 0.7, AuditSampleMin: 5}

	if got := deriveStatus(nil, cfg); got != StatusCandidate {
		t.Errorf("empty conditions: status = %q, want candidate", got)
	}
	allExploring := []ObservedCondition{{SuccessCount: 0, FailureCount: 1}}
	if got := deriveStatus(allExploring, cfg); got != StatusCandidate {
		t.Errorf("all-exploring: status = %q, want candidate", got)
	}
	oneSelfGraded := []ObservedCondition{
		{SuccessCount: 0, FailureCount: 1},
		{SuccessCount: 20, FailureCount: 0},
	}
	if got := deriveStatus(oneSelfGraded, cfg); got != StatusVerified {
		t.Errorf("one self_graded among many: status = %q, want verified", got)
	}
}

// TestMatcher_ExploringTier_SamplingBoundary covers explore_rate_low
// sampling with an injected fixed randFloat: a stub returning 0.0 (always
// "wins" the Bernoulli draw since 0.0 < explore_rate_low for any positive
// rate) must serve; a stub returning 1.0 (never wins) must not.
func TestMatcher_ExploringTier_SamplingBoundary(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	svc.SetConfidenceConfig(ConfidenceConfig{ServingConfidenceMin: 0.9, AuditSampleMin: 5, ExploreRateLow: 0.5})
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	_ = l
	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}

	matcher.randFloat = func() float64 { return 0.0 }
	matcher.InvalidateCache()
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exploring-tier condition to be sampled in (randFloat=0.0), got %+v", matches)
	}
	if matches[0].Tier != TierExploring {
		t.Errorf("tier = %q, want exploring", matches[0].Tier)
	}

	matcher.randFloat = func() float64 { return 1.0 }
	matcher.InvalidateCache()
	matches, err = matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected exploring-tier condition to be excluded when not sampled (randFloat=1.0), got %+v", matches)
	}
}

// TestMatcher_TrustedTier_AuditSampledBoundary covers the trusted-tier
// independent AuditSampled draw with an injected fixed randFloat.
func TestMatcher_TrustedTier_AuditSampledBoundary(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	svc.SetConfidenceConfig(ConfidenceConfig{
		ServingConfidenceMin: 0.5, AuditSampleMin: 1, ExploreRateTrusted: 0.5,
	})
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	cond := l.ObservedConditions[0]
	cond.SuccessCount = 50
	cond.AuditedSuccessCount = 50
	if err := store.ReplaceObservedConditions(l.LinkID, []ObservedCondition{cond}); err != nil {
		t.Fatalf("boost to trusted: %v", err)
	}

	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}

	matcher.randFloat = func() float64 { return 0.0 }
	matcher.InvalidateCache()
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].Tier != TierTrusted {
		t.Fatalf("expected 1 trusted match, got %+v", matches)
	}
	if !matches[0].AuditSampled {
		t.Errorf("expected AuditSampled=true with randFloat()=0.0 < explore_rate_trusted, got false")
	}

	matcher.randFloat = func() float64 { return 1.0 }
	matcher.InvalidateCache()
	matches, err = matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected trusted tier to always serve regardless of audit sampling draw, got %+v", matches)
	}
	if matches[0].AuditSampled {
		t.Errorf("expected AuditSampled=false with randFloat()=1.0, got true")
	}
}

// TestMatcher_OwningConditionRouting_RespectsTier covers the 2026-08-13
// change to the literal-question shortcut (docs/impl/v1/activation.md「字面
// 问题捷径与置信度档位」): a hit via known_question_terms must route through
// the SAME tiering as an ordinary four-tuple match, not bypass it — a
// condition dragged down to exploring by failures only serves probabilistically.
func TestMatcher_OwningConditionRouting_RespectsTier(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	svc.SetConfidenceConfig(ConfidenceConfig{ServingConfidenceMin: 0.7, AuditSampleMin: 5, ExploreRateLow: 0})
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	now := time.Now().UTC()
	question := "住宿费用怎么算"
	qq := text.Terms(text.Normalize(question))
	add := NormalizeObservedCondition("住宿", "", "", "", qq, now)
	if err := svc.AppendObservedCondition(l.LinkID, add, 50); err != nil {
		t.Fatalf("append: %v", err)
	}
	fresh, err := svc.GetLink(l.LinkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	// Drag the owning condition's confidence down to exploring.
	for i, c := range fresh.ObservedConditions {
		if len(c.KnownQuestionTerms) > 0 {
			fresh.ObservedConditions[i].FailureCount = 20
		}
	}
	if err := store.ReplaceObservedConditions(l.LinkID, fresh.ObservedConditions); err != nil {
		t.Fatalf("drag down confidence: %v", err)
	}

	query := session.ExpandedQuery{
		Subject: "完全不同", Intent: "完全不同", ExpandedQuestion: question,
	}
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	// ExploreRateLow=0 -> literal-question hit on a now-exploring-tier
	// condition must NOT serve, even though it would have bypassed
	// everything under the pre-2026-08-13 unconditional shortcut.
	if len(matches) != 0 {
		t.Fatalf("expected known-question shortcut to respect exploring tier (rate=0), got %+v", matches)
	}
}
