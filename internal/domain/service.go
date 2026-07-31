package domain

import (
	"fmt"
	"strings"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) List() ([]Domain, error) {
	return s.store.List()
}

func (s *Service) Create(name, description string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("domain: name is required")
	}
	return s.store.Create(name, strings.TrimSpace(description))
}
