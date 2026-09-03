// Package domain provides minimal CRUD over the domains table for the 知识领域
// page: domains themselves are flat structural metadata (not concept-evolution
// content), so unlike entry_candidates there is no candidate/confirm gate —
// creating one just inserts a row.
package domain

import "time"

// Domain is a domains row plus the counts the 知识领域 page needs to render
// without a second round trip per domain.
type Domain struct {
	DomainID           string    `json:"domain_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	ConceptCount       int       `json:"entry_count"`
	SourceCount        int       `json:"source_count"`
	KPCount            int       `json:"kp_count"`
	UnassignedKPCount  int       `json:"unassigned_kp_count"`
	PendingSignalCount int       `json:"pending_signal_count"`
	CreatedAt          time.Time `json:"created_at"`
}

// DocCategory is a doc_categories row plus how many sources currently carry
// it (docs/design/doc-category.md) — a closed, human-curated document-genre
// taxonomy scoped per domain, distinct from and not affected by the
// query-driven source_affinity "主题标签" mechanism.
type DocCategory struct {
	CategoryID  string    `json:"category_id"`
	DomainID    string    `json:"domain_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceCount int       `json:"source_count"`
	CreatedAt   time.Time `json:"created_at"`
}
