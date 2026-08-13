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

// TestDeriveAndPersistStatus_NotifiesWikiOnVerified locks docs/impl/v1/wiki.md
// 步骤5 触发(d), updated 2026-08-13 for the derived-status model (see
// docs/design/activation-convergence.md): deriveAndPersistStatus — the
// single place status transitions now happen — must call
// WikiNotifier.NotifyLinkVerified exactly when a link's derived status
// becomes verified, and must not fire again while it stays verified or when
// it drops back to candidate.
func TestDeriveAndPersistStatus_NotifiesWikiOnVerified(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))
	notifier := &fakeWikiNotifier{}
	svc.SetWikiNotifier(notifier)

	seedKPFull(t, db, "kp1")
	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	verified := verifyLink(t, svc, l)
	if verified.Status != StatusVerified {
		t.Fatalf("status = %q, want verified", verified.Status)
	}
	if len(notifier.pointIDs) != 1 || notifier.pointIDs[0] != "kp1" {
		t.Fatalf("expected NotifyLinkVerified(kp1) once, got %v", notifier.pointIDs)
	}

	// Recording another success on an already-verified link doesn't flip
	// status again (it's already verified) — no duplicate notify.
	cond := verified.ObservedConditions[0]
	if err := svc.RecordOutcome(l.LinkID, cond.Subject, cond.Intent, cond.Audience, cond.Constraint, true, ""); err != nil {
		t.Fatalf("record outcome: %v", err)
	}
	if len(notifier.pointIDs) != 1 {
		t.Fatalf("expected no additional notify while already verified, got %v", notifier.pointIDs)
	}

	// Reject clears conditions, dropping status back to candidate — no
	// notify for that direction.
	if _, err := svc.Reject(l.LinkID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if len(notifier.pointIDs) != 1 {
		t.Fatalf("expected no notify on drop back to candidate, got %v", notifier.pointIDs)
	}
}

// TestDeriveAndPersistStatus_NilWikiNotifierNoop confirms the unset-notifier
// default (used by every test/production wiring that hasn't called
// SetWikiNotifier) doesn't panic or otherwise change behavior.
func TestDeriveAndPersistStatus_NilWikiNotifierNoop(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := newTestService(store, NewMatcher(store))

	seedKPFull(t, db, "kp1")
	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "住宿"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)
}
