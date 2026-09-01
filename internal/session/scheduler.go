package session

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// Scheduler periodically evicts sessions older than the retention window.
// retentionDays and the ticker interval can both be updated live (系统设置 →
// 历史会话, no restart required) via SetRetentionDays/SetInterval.
type Scheduler struct {
	store         *Store
	retentionDays atomic.Int64
	interval      atomic.Int64 // time.Duration, nanoseconds
	resetInterval chan time.Duration
	stop          chan struct{}
	done          chan struct{}
}

func NewScheduler(store *Store, retentionDays int, interval time.Duration) *Scheduler {
	s := &Scheduler{
		store:         store,
		resetInterval: make(chan time.Duration, 1),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	s.retentionDays.Store(int64(retentionDays))
	s.interval.Store(int64(interval))
	return s
}

// SetRetentionDays updates the retention window used by the next (and all
// subsequent) cleanup runs.
func (s *Scheduler) SetRetentionDays(days int) {
	s.retentionDays.Store(int64(days))
}

// SetInterval updates the ticker interval, taking effect immediately.
func (s *Scheduler) SetInterval(interval time.Duration) {
	s.interval.Store(int64(interval))
	select {
	case s.resetInterval <- interval:
	default:
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

	ticker := time.NewTicker(time.Duration(s.interval.Load()))
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.executeOnce()
		case newInterval := <-s.resetInterval:
			ticker.Reset(newInterval)
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) executeOnce() {
	retentionDays := int(s.retentionDays.Load())
	n, err := s.store.DeleteOlderThan(retentionDays)
	if err != nil {
		slog.Error("session: retention cleanup failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("session: retention cleanup complete", "deleted", n, "retention_days", retentionDays)
	}
}
