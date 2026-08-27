package session

import (
	"log/slog"
	"time"
)

// Scheduler periodically evicts sessions older than the retention window.
type Scheduler struct {
	store         *Store
	retentionDays int
	interval      time.Duration
	stop          chan struct{}
	done          chan struct{}
}

func NewScheduler(store *Store, retentionDays int, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:         store,
		retentionDays: retentionDays,
		interval:      interval,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	go s.run()
}

func (s *Scheduler) Stop() {
	close(s.stop)
	<-s.done
}

func (s *Scheduler) run() {
	defer close(s.done)

	s.executeOnce()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.executeOnce()
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) executeOnce() {
	n, err := s.store.DeleteOlderThan(s.retentionDays)
	if err != nil {
		slog.Error("session: retention cleanup failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("session: retention cleanup complete", "deleted", n, "retention_days", s.retentionDays)
	}
}
