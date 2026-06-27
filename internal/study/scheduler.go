package study

import (
	"log/slog"
	"time"
)

type Scheduler struct {
	svc      *Service
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
}

func NewScheduler(svc *Service, interval time.Duration) *Scheduler {
	return &Scheduler{
		svc:      svc,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
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

	// 首次执行延迟 1 分钟
	select {
	case <-time.After(1 * time.Minute):
	case <-s.stop:
		return
	}

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
	start := time.Now()
	result, err := s.svc.Run()
	if err != nil {
		slog.Error("study: scheduled run failed", "error", err)
		return
	}
	slog.Info("study: scheduled run complete",
		"candidates_flagged", result.CandidatesFlagged,
		"gap_events_processed", result.GapEventsProcessed,
		"report_id", result.ReportID,
		"elapsed_ms", time.Since(start).Milliseconds())
}
