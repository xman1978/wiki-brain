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

func (s *Service) Update(domainID, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("domain: name is required")
	}
	return s.store.Update(domainID, name, strings.TrimSpace(description))
}

func (s *Service) ListDocCategories(domainID string) ([]DocCategory, error) {
	return s.store.ListDocCategories(domainID)
}

func (s *Service) CreateDocCategory(domainID, name, description string) (string, error) {
	domainID = strings.TrimSpace(domainID)
	name = strings.TrimSpace(name)
	if domainID == "" {
		return "", fmt.Errorf("domain: domain_id is required")
	}
	if name == "" {
		return "", fmt.Errorf("domain: name is required")
	}
	return s.store.CreateDocCategory(domainID, name, strings.TrimSpace(description))
}

func (s *Service) UpdateDocCategory(categoryID, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("domain: name is required")
	}
	return s.store.UpdateDocCategory(categoryID, name, strings.TrimSpace(description))
}

func (s *Service) DeleteDocCategory(categoryID string) error {
	return s.store.DeleteDocCategory(categoryID)
}
