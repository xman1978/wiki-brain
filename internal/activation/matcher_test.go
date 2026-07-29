package activation

import (
	"database/sql"
	"testing"
	"time"

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

	cond := LinkCondition{SubjectTerms: "住宿 费用", IntentTerms: []string{"标准"}}
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
	cond := LinkCondition{SubjectTerms: "住宿 费用", IntentTerms: []string{"标准"}, ConstraintTerms: []string{constraintTerms}}
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

func TestMatcher_ConstraintMustMatchExactly(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	// Compute the stored term string the same way Study would (Normalize +
	// Terms), rather than hand-writing a literal, so the test doesn't depend
	// on guessing exact gse segmentation output.
	constraintTerms := text.Terms(text.Normalize("产品甲"))
	cond := LinkCondition{SubjectTerms: "住宿 费用", ConstraintTerms: []string{constraintTerms}}
	l, err := svc.CreateLink("t1", cond, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	// Full-match semantics are symmetric: a query with an extra constraint
	// the link doesn't know about no longer passes (docs/impl/v1/activation.md
	// 步骤 2 修订版 — 取代旧版"守门方向不对称"）。
	query := session.ExpandedQuery{
		Subject:          "住宿 费用",
		Constraint:       "产品甲 加班",
		ExpandedQuestion: "住宿费用标准是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected query's extra constraint (superset of link's) to exclude the link, got %+v", matches)
	}

	// The exact same constraint text does match.
	query.Constraint = "产品甲"
	matches, err = matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected identical constraint to match, got %+v", matches)
	}
}

func TestMatcher_AudienceGating(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	cond := LinkCondition{SubjectTerms: "住宿", Audience: []string{"hr"}}
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

	// Fallback now requires question_terms to match the query's
	// expanded_question byte-for-byte after normalization (exact repeat, not
	// containment) — so both sides must derive from the identical raw
	// question, the same way Study computes traces.question_terms.
	rawQuestion := "出差漠河的住宿标准是多少"
	questionTerms := text.Terms(text.Normalize(rawQuestion))

	// Link created without subject_terms (e.g. legacy/degraded-session link).
	l1, err := svc.CreateLink(questionTerms, LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link1: %v", err)
	}
	verifyLink(t, svc, l1)

	// A gated link (has audience) must never be reachable via fallback, even
	// when its question_terms match exactly.
	l2, err := svc.CreateLink(questionTerms, LinkCondition{Audience: []string{"hr"}}, "kp2", nil)
	if err != nil {
		t.Fatalf("create link2: %v", err)
	}
	verifyLink(t, svc, l2)

	query := session.ExpandedQuery{
		ExpandedQuestion: rawQuestion,
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

func TestMatcher_IncludesCandidateExcludesWeakenedDeprecatedAndNonCurrentKP(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp-cand")
	seedKPFull(t, db, "kp-weak")
	seedKPFull(t, db, "kp-dep")
	seedKPFull(t, db, "kp-stale")
	setLifecycle(t, db, "kp-stale", "superseded")

	cand, err := svc.CreateLink("t-cand", LinkCondition{SubjectTerms: "住宿"}, "kp-cand", nil)
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	weak, err := svc.CreateLink("t-weak", LinkCondition{SubjectTerms: "住宿"}, "kp-weak", nil)
	if err != nil {
		t.Fatalf("create weak link: %v", err)
	}
	if _, err := svc.TransitionLink(weak.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := svc.TransitionLink(weak.LinkID, StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	dep, err := svc.CreateLink("t-dep", LinkCondition{SubjectTerms: "住宿"}, "kp-dep", nil)
	if err != nil {
		t.Fatalf("create dep link: %v", err)
	}
	if _, err := svc.TransitionLink(dep.LinkID, StatusDeprecated, "test", nil); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	stale, err := svc.CreateLink("t-stale", LinkCondition{SubjectTerms: "住宿"}, "kp-stale", nil)
	if err != nil {
		t.Fatalf("create stale: %v", err)
	}
	if _, err := svc.TransitionLink(stale.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("verify stale: %v", err)
	}

	query := session.ExpandedQuery{Subject: "住宿", ExpandedQuestion: "住宿标准"}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected only candidate (current KP) to match for signal recording, got %+v", matches)
	}
	if matches[0].Link.LinkID != cand.LinkID {
		t.Errorf("expected candidate link %s, got %s", cand.LinkID, matches[0].Link.LinkID)
	}
	if matches[0].Link.Status != StatusCandidate {
		t.Errorf("status = %q, want candidate", matches[0].Link.Status)
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
	if len(matches) != 1 || matches[0].Link.Status != StatusCandidate {
		t.Fatalf("expected candidate match after CreateLink invalidates cache, got %+v", matches)
	}

	// TransitionLink goes through Service, which calls matcher.InvalidateCache().
	if _, err := svc.TransitionLink(l.LinkID, StatusDeprecated, "test", nil); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	matches, err = matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match after deprecate: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected cache to reload and drop deprecated link, got %+v", matches)
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

// TestMatcher_SubjectOverlap_QuerySupersetOfCoreMatches covers the reason
// subject moved from exact equality to overlap: Study stores subject_terms
// as a cross-phrasing intersection core (LabelTermIntersection), which is by
// construction a *subset* of any single phrasing's own subject terms — so a
// query whose subject terms are a strict superset of the link's core must
// still match.
func TestMatcher_SubjectOverlap_QuerySupersetOfCoreMatches(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	// "数据库 句柄" is the intersection core of "数据库 句柄 限制" and
	// "数据库 句柄 管理" — shorter than either full phrasing.
	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "数据库 句柄"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{
		Subject:          "数据库 句柄 限制",
		ExpandedQuestion: "数据库句柄数限制是多少",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected the shorter stored core to match a query subject that's a superset of it, got %+v", matches)
	}
}

// TestMatcher_SubjectOverlap_CoreWordMissingFromQuery_Excluded covers the
// other side: a link core containing a word the query never mentions must
// not match, even if most words overlap — overlap means core ⊆ query, not
// "core and query share some words".
func TestMatcher_SubjectOverlap_CoreWordMissingFromQuery_Excluded(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "数据库 句柄 限制"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	query := session.ExpandedQuery{
		Subject:          "数据库 句柄",
		ExpandedQuestion: "数据库句柄是什么",
	}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected the query to be missing the core word '限制' and not match, got %+v", matches)
	}
}

// TestMatcher_AudienceEitherObservedGroupMatches: two historically observed
// audiences are stored as separate condition groups (not a whitelist union);
// a query matching either group's audience activates the link.
func TestMatcher_AudienceEitherObservedGroupMatches(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	now := time.Now().UTC()
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: "住宿", Intent: "", Audience: "hr", Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
			{Subject: "住宿", Intent: "", Audience: text.NormalizeCompact("行政"), Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	for _, audience := range []string{"hr", "行政"} {
		t.Run(audience, func(t *testing.T) {
			query := session.ExpandedQuery{
				Subject:          "住宿",
				Audience:         audience,
				ExpandedQuestion: "住宿标准",
			}
			matches, err := matcher.Match(query, MatchConfig{})
			if err != nil {
				t.Fatalf("match: %v", err)
			}
			if len(matches) != 1 {
				t.Errorf("expected observed audience %q to match, got %+v", audience, matches)
			}
		})
	}

	query := session.ExpandedQuery{Subject: "住宿", Audience: "财务", ExpandedQuestion: "住宿标准"}
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected an audience never observed for this point to still be excluded, got %+v", matches)
	}
}

// TestMatcher_NoCrossProductAcrossGroups: subject from group A + intent from
// group B must NOT match (observed combinations only).
func TestMatcher_NoCrossProductAcrossGroups(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	now := time.Now().UTC()
	qiA := text.Terms(text.Normalize("查询期限"))
	qiB := text.Terms(text.Normalize("查询标准"))
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: "招待费报销", Intent: qiA, Audience: "", Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
			{Subject: "差旅报销", Intent: qiB, Audience: "", Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	verifyLink(t, svc, l)

	// Cross: subject of A + intent of B
	matches, err := matcher.Match(session.ExpandedQuery{
		Subject: "招待费报销", Intent: "查询标准", ExpandedQuestion: "x",
	}, MatchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("cross-product must not match, got %+v", matches)
	}

	// Exact group A
	matches, err = matcher.Match(session.ExpandedQuery{
		Subject: "招待费报销", Intent: "查询期限", ExpandedQuestion: "x",
	}, MatchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("exact group A should match, got %+v", matches)
	}
}

// TestMatcher_SubjectCoreMatchesAcrossSubjectAndIntent covers a gap found by
// testing against real production data (not hypothetical): the core word
// that got into a link's subject_terms because one historical trace's
// Session parse put it in the `subject` slot can show up in `intent` instead
// for a fresh, on-topic rephrasing — Session doesn't reliably put the same
// concept in the same slot every time. Checking the core against
// subject+intent combined (not subject alone) catches this without loosening
// what Study is willing to put in the core in the first place.
func TestMatcher_SubjectCoreMatchesAcrossSubjectAndIntent(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	// "扣分" is absent from Subject but present in Intent this time — the
	// core word is still genuinely present in the question, just in a
	// different slot than whichever historical trace it was learned from.
	query := session.ExpandedQuery{
		Subject:          "培训考勤管理",
		Intent:           "查询考勤扣分规则",
		ExpandedQuestion: "培训考勤扣分是怎么规定的",
	}
	// IntentTerms includes the query's own (normalized) intent so this test
	// isolates the core-matching fix — intent whitelist membership is a
	// separate, unrelated gate covered by TestMatcher_ExactQuadrupleReproduced_ScoresOne.
	l, err := svc.CreateLink("t1", LinkCondition{
		SubjectTerms: "培训 扣分 考勤",
		IntentTerms:  []string{text.Terms(text.Normalize(query.Intent))},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)
	matches, err := matcher.Match(query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected core word found via intent (not subject) to still match, got %+v", matches)
	}
}

// TestMatcher_SubjectCoreSurvivesTokenizationBoundaryDrift covers the other
// real gap: the gse segmenter doesn't always draw the same word boundary for
// the same compound noun. A core word stored as one token ("数据库连接") must
// still match a query where the identical character sequence appears but the
// tokenizer happened to split it into two tokens this time — checked via
// substring containment on the raw normalized text, not token-set membership,
// so tokenizer boundary placement can't cause a miss.
func TestMatcher_SubjectCoreSurvivesTokenizationBoundaryDrift(t *testing.T) {
	if !coreContained(map[string]struct{}{"数据库连接": {}}, "数据库连接配置") {
		t.Error("expected core word to match via substring containment despite the query's own tokenizer splitting it differently")
	}
	if coreContained(map[string]struct{}{"数据库连接限制": {}}, "数据库连接配置") {
		t.Error("expected a core word that isn't actually a substring to still be excluded")
	}
}

// seedActiveSynonym inserts a status=active subject_synonyms row directly
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md),
// standing in for either a preset-loaded alias or a confirmed gap-mined
// candidate — Match doesn't distinguish by source.
func seedActiveSynonym(t *testing.T, db *sql.DB, term, canonical string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO subject_synonyms (synonym_id, term, canonical, source, status)
		VALUES (?, ?, ?, 'preset', 'active')`, "syn-"+term, term, canonical)
	if err != nil {
		t.Fatalf("seed active synonym %q->%q: %v", term, canonical, err)
	}
}

// TestMatcher_SubjectSynonymCanonicalizesBothSides covers the core same-KP
// scenario the synonym dictionary exists for: a link observed one wording,
// and a differently-worded (but registered as synonymous) question should
// still hit it — without loosening intent/audience/constraint at all.
func TestMatcher_SubjectSynonymCanonicalizesBothSides(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")
	seedActiveSynonym(t, db, text.Normalize("证券市场"), text.Normalize("股票市场"))

	now := time.Now().UTC()
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: text.Normalize("股票市场"), Intent: "", Audience: "", Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	matches, err := matcher.Match(session.ExpandedQuery{
		Subject: "证券市场", ExpandedQuestion: "证券市场怎么运作",
	}, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected synonym-registered subject wording to match, got %+v", matches)
	}
}

// TestMatcher_SubjectSynonymDoesNotLoosenOtherDimensions ensures the
// canonicalization is scoped to subject only: a query whose subject is
// synonym-equivalent but whose constraint differs from every observed group
// must still miss.
func TestMatcher_SubjectSynonymDoesNotLoosenOtherDimensions(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")
	seedActiveSynonym(t, db, text.Normalize("证券市场"), text.Normalize("股票市场"))

	now := time.Now().UTC()
	constraintTerms := text.Terms(text.Normalize("产品甲"))
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: text.Normalize("股票市场"), Intent: "", Audience: "", Constraint: constraintTerms, FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	matches, err := matcher.Match(session.ExpandedQuery{
		Subject: "证券市场", Constraint: "产品乙", ExpandedQuestion: "证券市场怎么运作",
	}, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected constraint mismatch to still exclude despite subject synonym match, got %+v", matches)
	}
}

// TestMatcher_SubjectOnlyMiss_DetectsCandidateWhenOtherDimensionsAgree covers
// the diagnostic Trace's near-miss detection relies on to mine
// subject_synonym_gap candidates: intent/audience/constraint all agree with
// an observed group, but the subject wording isn't registered as synonymous
// (yet) — SubjectOnlyMiss should surface that group's observed subject.
func TestMatcher_SubjectOnlyMiss_DetectsCandidateWhenOtherDimensionsAgree(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	now := time.Now().UTC()
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: text.Normalize("招待费报销"), Intent: "", Audience: "", Constraint: "", FirstSeenAt: now, LastSeenAt: now, HitCount: 3},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	// No synonym registered yet — Match itself should miss.
	matches, err := matcher.Match(session.ExpandedQuery{
		Subject: "差旅报销", ExpandedQuestion: "差旅报销怎么处理",
	}, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no synonym registered to still miss on Match, got %+v", matches)
	}

	observed, ok := matcher.SubjectOnlyMiss(l.ObservedConditions, "差旅报销", "", "", "")
	if !ok {
		t.Fatal("expected SubjectOnlyMiss to surface a subject-only-miss candidate")
	}
	if observed != text.Normalize("招待费报销") {
		t.Errorf("observedSubject = %q, want %q", observed, text.Normalize("招待费报销"))
	}
}

// TestMatcher_SubjectOnlyMiss_NoCandidateWhenOtherDimensionsDiffer ensures
// SubjectOnlyMiss doesn't fire when the mismatch isn't subject-only — a
// different constraint should not be reported as a synonym candidate.
func TestMatcher_SubjectOnlyMiss_NoCandidateWhenOtherDimensionsDiffer(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := NewService(store, matcher)
	seedKPFull(t, db, "kp1")

	now := time.Now().UTC()
	constraintTerms := text.Terms(text.Normalize("产品甲"))
	l, err := svc.CreateLink("t1", LinkCondition{
		ObservedConditions: []ObservedCondition{
			{Subject: text.Normalize("招待费报销"), Intent: "", Audience: "", Constraint: constraintTerms, FirstSeenAt: now, LastSeenAt: now, HitCount: 1},
		},
	}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	_, ok := matcher.SubjectOnlyMiss(l.ObservedConditions, "差旅报销", "", "", "产品乙")
	if ok {
		t.Error("expected SubjectOnlyMiss to stay silent when constraint also differs (not a subject-only miss)")
	}
}
