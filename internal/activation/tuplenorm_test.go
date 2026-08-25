package activation

import (
	"context"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestTupleNormalizer_Tier1ExactMatch_TouchesLastHit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	n := NewTupleNormalizer(store, TupleNormConfig{})

	ctx := context.Background()
	// First ask creates the canonical record.
	s1, i1, a1, c1, _, _, err := n.Normalize(ctx, []string{"dom1"}, "奖金制度", "查询规则", "全体员工", "2026年")
	if err != nil {
		t.Fatalf("first normalize: %v", err)
	}

	before, err := store.FindExactMatch([]string{"dom1"}, s1, i1, a1, c1)
	if err != nil || before == nil {
		t.Fatalf("expected inserted row, err=%v", err)
	}
	oldHit := before.LastHitAt

	time.Sleep(10 * time.Millisecond)

	// Second ask, exact same normalized tuple, should hit tier 1 and touch last_hit_at.
	s2, i2, a2, c2, _, _, err := n.Normalize(ctx, []string{"dom1"}, "奖金制度", "查询规则", "全体员工", "2026年")
	if err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if s2 != s1 || i2 != i1 || a2 != a1 || c2 != c1 {
		t.Fatalf("expected identical canonical tuple, got %q/%q/%q/%q", s2, i2, a2, c2)
	}

	after, err := store.FindExactMatch([]string{"dom1"}, s1, i1, a1, c1)
	if err != nil || after == nil {
		t.Fatalf("expected row still present, err=%v", err)
	}
	if !after.LastHitAt.After(oldHit) {
		t.Fatalf("expected last_hit_at touched: before=%v after=%v", oldHit, after.LastHitAt)
	}

	// Only one row should exist (tier 1 hit, no new insert).
	cands, err := store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 row, got %d", len(cands))
	}
}

func TestTupleNormalizer_Tier2LocalSimilarity_AboveAndBelowThreshold(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	n := NewTupleNormalizer(store, TupleNormConfig{LocalSimMin: 0.5})

	ctx := context.Background()
	// Seed a canonical tuple.
	if _, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "年假申请流程说明", "如何申请", "全体员工", ""); err != nil {
		t.Fatalf("seed normalize: %v", err)
	}

	// A near-duplicate wording (high token overlap) should hit tier 2 and
	// return the canonical tuple, not create a new row.
	s2, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "年假申请流程", "如何申请", "全体员工", "")
	if err != nil {
		t.Fatalf("near-dup normalize: %v", err)
	}
	if s2 != "年假申请流程说明" {
		t.Fatalf("expected tier2 hit to return canonical subject, got %q", s2)
	}
	cands, err := store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected tier2 hit not to insert a new row, got %d rows", len(cands))
	}

	// A clearly unrelated tuple should miss tier 1/2 and (no LLM client wired)
	// fall through to insert a new record.
	s3, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "差旅报销标准", "查询限额", "出差员工", "")
	if err != nil {
		t.Fatalf("unrelated normalize: %v", err)
	}
	if s3 == "年假申请流程说明" {
		t.Fatalf("unrelated tuple should not have matched the existing canonical row")
	}
	cands, err = store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected a new row for the unrelated tuple, got %d rows", len(cands))
	}
}

func TestTupleNormalizer_NoLocalSimMatch_FallsThroughToLLMTier(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	n := NewTupleNormalizer(store, TupleNormConfig{LocalSimMin: 0.99})
	fake := llm.NewFakeClient()
	fake.SetResponse("tuple_norm_match.md", llm.FakeResponse{Output: `{"matched":true,"candidate_index":0}`})
	n.SetLLMClient(fake)

	ctx := context.Background()
	if _, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "差旅报销标准是什么", "查询", "员工", ""); err != nil {
		t.Fatalf("seed normalize: %v", err)
	}

	s2, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "差旅费报销规定", "咨询", "在职人员", "")
	if err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if s2 != "差旅报销标准是什么" {
		t.Fatalf("expected LLM tier match to return canonical subject, got %q", s2)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", len(calls))
	}
}

func TestTupleNormalizer_LLMNoMatch_InsertsNewRecord(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	n := NewTupleNormalizer(store, TupleNormConfig{LocalSimMin: 0.99})
	fake := llm.NewFakeClient()
	fake.SetResponse("tuple_norm_match.md", llm.FakeResponse{Output: `{"matched":false,"candidate_index":-1}`})
	n.SetLLMClient(fake)

	ctx := context.Background()
	if _, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "差旅报销标准", "查询", "员工", ""); err != nil {
		t.Fatalf("seed normalize: %v", err)
	}

	if _, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1"}, "年假申请流程", "查询", "员工", ""); err != nil {
		t.Fatalf("second normalize: %v", err)
	}

	cands, err := store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 rows (LLM no-match falls through to new record), got %d", len(cands))
	}
}

func TestTupleNormalizer_NewRecord_OneRowPerDomain(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	n := NewTupleNormalizer(store, TupleNormConfig{})

	ctx := context.Background()
	if _, _, _, _, _, _, err := n.Normalize(ctx, []string{"dom1", "dom2"}, "奖金制度", "查询", "全体员工", ""); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	c1, err := store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list dom1: %v", err)
	}
	if len(c1) != 1 {
		t.Fatalf("expected 1 row in dom1, got %d", len(c1))
	}
	c2, err := store.ListCandidatesByDomain([]string{"dom2"}, 10)
	if err != nil {
		t.Fatalf("list dom2: %v", err)
	}
	if len(c2) != 1 {
		t.Fatalf("expected 1 row in dom2, got %d", len(c2))
	}
}

func TestStore_DeleteIdleOlderThan_OnlyDeletesOldRows(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	now := time.Now().UTC()
	old := &QuestionTupleNorm{DomainID: "dom1", Subject: "old", Intent: "i", Audience: "a", ConstraintText: "c", LastHitAt: now.AddDate(0, 0, -100), CreatedAt: now.AddDate(0, 0, -100)}
	fresh := &QuestionTupleNorm{DomainID: "dom1", Subject: "fresh", Intent: "i", Audience: "a", ConstraintText: "c", LastHitAt: now, CreatedAt: now}
	if err := store.InsertTupleNorm(old); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if err := store.InsertTupleNorm(fresh); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}

	n, err := store.DeleteIdleOlderThan(90)
	if err != nil {
		t.Fatalf("delete idle: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}

	remaining, err := store.ListCandidatesByDomain([]string{"dom1"}, 10)
	if err != nil {
		t.Fatalf("list remaining: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Subject != "fresh" {
		t.Fatalf("expected only 'fresh' to remain, got %+v", remaining)
	}
}
