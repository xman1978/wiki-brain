package wiki

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
)

// Catalog card kinds / display statuses for GET /wiki/catalog.
const (
	CatalogKindPage       = "page"
	CatalogKindCandidate  = "candidate"
	CatalogKindWizardTask = "wizard_task"

	// CatalogStatusPendingCompile is a pending_confirm wiki_candidate that
	// has not produced a wiki_pages row yet. UI label: 待编译.
	CatalogStatusPendingCompile = "pending_compile"
)

// CatalogDomain is one knowledge-domain bucket on the Wiki page's left rail,
// with every visible wiki card that belongs to that domain.
type CatalogDomain struct {
	DomainID    string        `json:"domain_id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	WikiCount   int           `json:"wiki_count"`
	Pages       []CatalogCard `json:"pages"`
}

// CatalogCard is one right-pane card: either an existing wiki_pages row or a
// pending Study wiki_candidate. Topic pages may appear under every member
// concept's domain (same page_id, multiple domain buckets).
type CatalogCard struct {
	Kind        string `json:"kind"`                // page | candidate | wizard_task
	PageID      string `json:"page_id,omitempty"`   // kind=page
	PageType    string `json:"page_type,omitempty"` // concept | fact | topic
	EntryID     string `json:"entry_id,omitempty"`  // concept pages / candidates
	ResultID    string `json:"result_id,omitempty"` // kind=candidate
	TaskID      string `json:"task_id,omitempty"`   // kind=wizard_task
	Title       string `json:"title"`               // 主题
	Description string `json:"description"`         // 说明
	Status      string `json:"status"`              // pending_compile|draft|needs_recompile|published|archived|candidates_loading|candidates_ready|error
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type catalogRow struct {
	card      CatalogCard
	domainID  string
	updatedAt time.Time
}

// ListCatalog groups wiki cards by knowledge domain for the Wiki drawer.
// Concept pages and pending wiki_candidates land in their concept's domain;
// topic pages land in every distinct domain of their contains members.
// Archived pages are included (UI label 已删除). Candidates that already have
// a non-archived page for the same concept are skipped.
func (s *Store) ListCatalog() ([]CatalogDomain, error) {
	domains, err := s.listCatalogDomains()
	if err != nil {
		return nil, err
	}
	byDomain := make(map[string][]catalogRow, len(domains))

	entryRows, err := s.listCatalogEntryPages()
	if err != nil {
		return nil, err
	}
	for _, r := range entryRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	candRows, err := s.listCatalogCandidates()
	if err != nil {
		return nil, err
	}
	for _, r := range candRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	out := make([]CatalogDomain, 0, len(domains))
	for _, d := range domains {
		rows := byDomain[d.DomainID]
		sortCatalogRows(rows)
		pages := make([]CatalogCard, 0, len(rows))
		for _, r := range rows {
			pages = append(pages, r.card)
		}
		d.WikiCount = len(pages)
		d.Pages = pages
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) listCatalogDomains() ([]CatalogDomain, error) {
	rows, err := s.db.Query(`
		SELECT domain_id, name, COALESCE(description, '')
		FROM domains
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("wiki store: catalog domains: %w", err)
	}
	defer rows.Close()

	var domains []CatalogDomain
	for rows.Next() {
		var d CatalogDomain
		if err := rows.Scan(&d.DomainID, &d.Name, &d.Description); err != nil {
			return nil, fmt.Errorf("wiki store: scan catalog domain: %w", err)
		}
		d.Pages = []CatalogCard{}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

// listCatalogEntryPages lists every published-or-not wiki page for the
// catalog's per-domain buckets (docs/impl/v1/wiki-single-tier-task-brief.md:
// single-tier Wiki has only one page_type, so there is no more "topic pages
// listed separately" split).
func (s *Store) listCatalogEntryPages() ([]catalogRow, error) {
	rows, err := s.db.Query(`
		SELECT p.page_id, p.page_type, p.entry_id, p.title, p.status, p.summary,
			p.updated_at, c.domain_id, COALESCE(c.description, '')
		FROM wiki_pages p
		JOIN entries c ON c.entry_id = p.entry_id
		ORDER BY p.updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("wiki store: catalog entry pages: %w", err)
	}
	defer rows.Close()

	var out []catalogRow
	for rows.Next() {
		var (
			pageID, pageType, title, status, summary, domainID, conceptDesc string
			conceptID                                                       sql.NullString
			updatedAt                                                       time.Time
		)
		if err := rows.Scan(&pageID, &pageType, &conceptID, &title, &status, &summary,
			&updatedAt, &domainID, &conceptDesc); err != nil {
			return nil, fmt.Errorf("wiki store: scan catalog concept page: %w", err)
		}
		desc := strings.TrimSpace(summary)
		if desc == "" {
			desc = strings.TrimSpace(conceptDesc)
		}
		card := CatalogCard{
			Kind:        CatalogKindPage,
			PageID:      pageID,
			PageType:    pageType,
			Title:       title,
			Description: desc,
			Status:      status,
			UpdatedAt:   updatedAt.Format(time.RFC3339),
		}
		if conceptID.Valid {
			card.EntryID = conceptID.String
		}
		out = append(out, catalogRow{card: card, domainID: domainID, updatedAt: updatedAt})
	}
	return out, rows.Err()
}

func (s *Store) listCatalogCandidates() ([]catalogRow, error) {
	rows, err := s.db.Query(`
		SELECT lr.result_id, lr.object_id, lr.reason, lr.updated_at,
			c.domain_id, c.name, COALESCE(c.description, '')
		FROM learning_results lr
		JOIN entries c ON c.entry_id = lr.object_id
		WHERE lr.action = ? AND lr.object_type = ? AND lr.status = ?
		  AND NOT EXISTS (
			SELECT 1 FROM wiki_pages p
			WHERE p.entry_id = lr.object_id AND p.status != ?
		  )
		ORDER BY lr.updated_at DESC`,
		activation.ActionWikiCandidate, activation.ObjectTypeWikiPage,
		activation.ResultPendingConfirm, StatusArchived)
	if err != nil {
		return nil, fmt.Errorf("wiki store: catalog candidates: %w", err)
	}
	defer rows.Close()

	var out []catalogRow
	for rows.Next() {
		var (
			resultID, conceptID, reason, domainID, name, conceptDesc string
			updatedAt                                                time.Time
		)
		if err := rows.Scan(&resultID, &conceptID, &reason, &updatedAt,
			&domainID, &name, &conceptDesc); err != nil {
			return nil, fmt.Errorf("wiki store: scan catalog candidate: %w", err)
		}
		desc := strings.TrimSpace(conceptDesc)
		if desc == "" {
			desc = strings.TrimSpace(reason)
		}
		out = append(out, catalogRow{
			domainID:  domainID,
			updatedAt: updatedAt,
			card: CatalogCard{
				Kind:        CatalogKindCandidate,
				EntryID:     conceptID,
				ResultID:    resultID,
				Title:       name,
				Description: desc,
				Status:      CatalogStatusPendingCompile,
				UpdatedAt:   updatedAt.Format(time.RFC3339),
			},
		})
	}
	return out, rows.Err()
}

func sortCatalogRows(rows []catalogRow) {
	rank := map[string]int{
		CatalogStatusPendingCompile: 0,
		StatusDraft:                 1,
		StatusNeedsRecompile:        2,
		StatusPublished:             3,
		StatusArchived:              4,
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rank[rows[i].card.Status], rank[rows[j].card.Status]
		if ri != rj {
			return ri < rj
		}
		return rows[i].updatedAt.After(rows[j].updatedAt)
	})
}
