package sysconfig

import "github.com/jxman78/wiki-brain/internal/session"

// Service wraps Store and pushes updated settings into the running
// consumers that were built from them at startup, so a save from 系统设置
// takes effect immediately — no restart, mirroring internal/llmconfig's
// Service.ReloadRouter pattern.
type Service struct {
	store          *Store
	fileViewClient *DynamicFileViewClient
	sessionSched   *session.Scheduler
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// SetFileViewClient wires the DynamicFileViewClient that SaveFileView
// pushes updates into. Called once from cmd/server/main.go after
// constructing it.
func (s *Service) SetFileViewClient(c *DynamicFileViewClient) {
	s.fileViewClient = c
}

// SetSessionScheduler wires the session.Scheduler that SaveSession pushes
// updates into. Called once from cmd/server/main.go after constructing it.
func (s *Service) SetSessionScheduler(sched *session.Scheduler) {
	s.sessionSched = sched
}

func (s *Service) GetFileView() (FileViewSettings, error) {
	return s.store.GetFileView()
}

func (s *Service) SaveFileView(v FileViewSettings) (FileViewSettings, error) {
	if err := ValidateFileView(&v); err != nil {
		return FileViewSettings{}, err
	}
	if err := s.store.SetFileView(v); err != nil {
		return FileViewSettings{}, err
	}
	if s.fileViewClient != nil {
		s.fileViewClient.Update(v)
	}
	return v, nil
}

func (s *Service) GetHelpManualHash() (string, error) {
	return s.store.GetHelpManualHash()
}

func (s *Service) SetHelpManualHash(hash string) error {
	return s.store.SetHelpManualHash(hash)
}

func (s *Service) GetSession() (SessionSettings, error) {
	return s.store.GetSession()
}

func (s *Service) SaveSession(v SessionSettings) (SessionSettings, error) {
	if err := ValidateSession(&v); err != nil {
		return SessionSettings{}, err
	}
	if err := s.store.SetSession(v); err != nil {
		return SessionSettings{}, err
	}
	if s.sessionSched != nil {
		s.sessionSched.SetRetentionDays(v.RetentionDays)
		s.sessionSched.SetInterval(v.Duration())
	}
	return v, nil
}
