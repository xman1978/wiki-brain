package activation

import (
	"context"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/session"
)

func TestBundleMatcher_ExactMatch(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewBundleMatcher(store)

	now := time.Now().UTC()
	cond := NormalizeObservedCondition("绩效考核", "怎么算", "", "", "", now)
	b := &ActivationBundle{ClusterFingerprint: "fp1", ObservedConditions: []ObservedCondition{cond}, Members: []BundleMember{{PointID: "p1"}, {PointID: "p2"}}}
	if err := store.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	query := session.ExpandedQuery{Subject: "绩效考核", Intent: "怎么算", ExpandedQuestion: "绩效考核怎么算"}
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].MatchedBy != MatchedByExact {
		t.Fatalf("matches = %+v, want one exact hit", matches)
	}
}

// TestBundleMatcher_SubjectMustMatchExactly covers the 2026-08-12 修订: a
// jittered subject (no longer synonym/containment matched) must miss.
func TestBundleMatcher_SubjectMustMatchExactly(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewBundleMatcher(store)

	now := time.Now().UTC()
	cond := NormalizeObservedCondition("绩效考核规则", "怎么算", "", "", "", now)
	b := &ActivationBundle{ClusterFingerprint: "fp1", ObservedConditions: []ObservedCondition{cond}, Members: []BundleMember{{PointID: "p1"}, {PointID: "p2"}}}
	if err := store.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	query := session.ExpandedQuery{Subject: "绩效考核细则", Intent: "怎么算", ExpandedQuestion: "绩效考核细则怎么算"}
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none (jittered subject no longer matches)", matches)
	}
}

func TestBundleMatcher_ExcludesDeprecatedFromCache(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewBundleMatcher(store)

	now := time.Now().UTC()
	cond := NormalizeObservedCondition("绩效考核", "怎么算", "", "", "", now)
	b := &ActivationBundle{ClusterFingerprint: "fp1", ObservedConditions: []ObservedCondition{cond}, Members: []BundleMember{{PointID: "p1"}}}
	if err := store.CreateBundle(b); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := store.UpdateBundleStatus(b.BundleID, BundleStatusDeprecated); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	query := session.ExpandedQuery{Subject: "绩效考核", Intent: "怎么算", ExpandedQuestion: "绩效考核怎么算"}
	matches, err := matcher.Match(context.Background(), query, MatchConfig{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none (deprecated bundle must not be matchable)", matches)
	}
}
