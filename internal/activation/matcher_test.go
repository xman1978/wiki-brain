package activation

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

func verifyLink(t *testing.T, svc *Service, l *ActivationLink) *ActivationLink {
	t.Helper()
	updated, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil)
	if err != nil {
		t.Fatalf("promote %s: %v", l.LinkID, err)
	}
	return updated
}

func TestMatcher_ExactQuadrupleReproduced_ScoresOne(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "住宿 费用", IntentTerms: "标准"}
	l, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{
		Subject:          "住宿 费用",
		Intent:           "标准",
		ExpandedQuestion: "住宿费用标准是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(matches), matches)
	}
	if matches[0].Score != 1.0 {
		t.Errorf("score = %f, want 1.0", matches[0].Score)
	}
}

func TestMatcher_ConstraintMismatch_ExcludedDespiteSubjectIntentMatch(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	constraintTerms := text.Terms(text.Normalize("产品甲"))
	cond := LinkCondition{SubjectTerms: "住宿 费用", IntentTerms: "标准", ConstraintTerms: constraintTerms}
	l, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{
		Subject:          "住宿 费用",
		Intent:           "标准",
		Constraint:       "产品乙",
		ExpandedQuestion: "住宿费用标准是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected constraint mismatch to exclude link, got %+v", matches)
	}
}

func TestMatcher_ConstraintCoverage_ExtraQueryConstraintsDoNotBlock(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	// Compute the stored term string the same way Study would (Normalize +
	// Terms), rather than hand-writing a literal, so the test doesn't depend
	// on guessing exact gse segmentation output.
	constraintTerms := text.Terms(text.Normalize("产品甲"))
	cond := LinkCondition{SubjectTerms: "住宿 费用", ConstraintTerms: constraintTerms}
	l, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{
		Subject:          "住宿 费用",
		Constraint:       "产品甲 加班",
		ExpandedQuestion: "住宿费用标准是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected link's constraint (subset of query's) to pass gate, got %+v", matches)
	}
}

func TestMatcher_AudienceGating(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "住宿", Audience: "hr"}
	l, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	cases := []struct {
		name     string
		audience string
		want     int
	}{
		{"query audience empty, link scoped -> excluded", "", 0},
		{"query audience mismatched -> excluded", "员工", 0},
		{"query audience matches -> included", "HR", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := session.ExpandedQuery{
				Subject:          "住宿",
				Audience:         tc.audience,
				ExpandedQuestion: "住宿标准",
			}
			matches, err := matcher.Match(query, MatchConfig{})
			if err != nil {
				t.Fatalf("match: %v", err)
			}
			if len(matches) != tc.want {
				t.Errorf("got %d matches, want %d: %+v", len(matches), tc.want, matches)
			}
		})
	}
}

func TestMatcher_FallbackWhenQuadrupleMissing(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")
	seedKPFull(t, db, "kp2")

	// Link created without subject_terms (e.g. legacy/degraded-session link).
	l1, err := svc.CreateLink("出差 漠河 住宿 标准", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link1: %v", err)
	}
	verifyLink(t, svc, l1)

	// A gated link (has audience) must never be reachable via fallback.
	l2, err := svc.CreateLink("出差 漠河 住宿 标准 限定", LinkCondition{Audience: "hr"}, "kp2", nil)
	if err != nil {
		t.Fatalf("create link2: %v", err)
	}
	verifyLink(t, svc, l2)

	query := session.ExpandedQuery{
		ExpandedQuestion: "出差漠河的住宿标准是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected only the ungated link to match via fallback, got %+v", matches)
	}
	if matches[0].Link.LinkID != l1.LinkID {
		t.Errorf("unexpected match: %+v", matches[0])
	}
}

func TestMatcher_ExcludesNonVerifiedAndNonCurrentKP(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	// candidate link never verified — must not match.
	if _, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil); err != nil {
		t.Fatalf("create link: %v", err)
	}

	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("candidate link should not participate in matching, got %+v", matches)
	}
}

func TestMatcher_CacheInvalidatesOnTransition(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches before promotion, got %+v", matches)
	}

	// TransitionLink goes through Service, which calls matcher.InvalidateCache().
	verifyLink(t, svc, l)

	matches, err = matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match after promotion: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected cache to reload and find the newly verified link, got %+v", matches)
	}
}

func TestMatcher_CacheInvalidatesOnLifecycleChange(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match before lifecycle change, got %+v", matches)
	}

	setLifecycle(t, db, "kp1", "superseded")
	if err := svc.InvalidateCache(); err != nil {
		t.Fatalf("invalidate cache: %v", err)
	}

	matches, err = matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match after lifecycle change: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches once target KP is no longer current, got %+v", matches)
	}
}
