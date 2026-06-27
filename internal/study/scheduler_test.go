package study

import (
	"testing"
	"time"
)

func TestScheduler_StopBeforeFirstRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig())

	scheduler := NewScheduler(svc, 1*time.Hour)
	scheduler.Start()

	// Stop immediately — should not block
	scheduler.Stop()
}

func TestScheduler_ExecuteOnce(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig())

	scheduler := NewScheduler(svc, 1*time.Hour)

	// Direct call to executeOnce (no need to wait for ticker)
	scheduler.executeOnce()

	// Should have generated a report
	reports, err := store.ListReports()
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("expected 1 report after executeOnce, got %d", len(reports))
	}
}
