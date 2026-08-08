package activation

import "testing"

// fakeWikiNotifier records every NotifyLinkVerified call for assertion.
type fakeWikiNotifier struct {
	pointIDs []string
}

func (f *fakeWikiNotifier) NotifyLinkVerified(pointID string) error {
	f.pointIDs = append(f.pointIDs, pointID)
	return nil
}

// TestTransitionLink_NotifiesWikiOnVerified locks docs/impl/v1/wiki.md 步骤5
// 触发(d): TransitionLink must call WikiNotifier.NotifyLinkVerified exactly
// when a link transitions to verified (candidate->verified or
// weakened->verified), covering the manual Confirm and Study
// promote/reverify call sites through the single TransitionLink entry point,
// and must not fire for other transitions (e.g. verified->weakened).
func TestTransitionLink_NotifiesWikiOnVerified(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	notifier := &fakeWikiNotifier{}
	svc.SetWikiNotifier(notifier)

	seedKPFull(t, db, "kp1")
	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote to verified: %v", err)
	}
	if len(notifier.pointIDs) != 1 || notifier.pointIDs[0] != "kp1" {
		t.Fatalf("expected NotifyLinkVerified(kp1) once, got %v", notifier.pointIDs)
	}

	if _, err := svc.TransitionLink(l.LinkID, StatusWeakened, "test", nil); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	if len(notifier.pointIDs) != 1 {
		t.Fatalf("expected no additional notify on verified->weakened, got %v", notifier.pointIDs)
	}

	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("reverify: %v", err)
	}
	if len(notifier.pointIDs) != 2 || notifier.pointIDs[1] != "kp1" {
		t.Fatalf("expected NotifyLinkVerified(kp1) again on reverify, got %v", notifier.pointIDs)
	}
}

// TestTransitionLink_NilWikiNotifierNoop confirms the unset-notifier default
// (used by every test/production wiring that hasn't called SetWikiNotifier)
// doesn't panic or otherwise change TransitionLink's behavior.
func TestTransitionLink_NilWikiNotifierNoop(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))

	seedKPFull(t, db, "kp1")
	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote to verified with no notifier set: %v", err)
	}
}
