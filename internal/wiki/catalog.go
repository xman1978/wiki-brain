package wiki

import (
	"database/sql"
	"encoding/json"
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

	conceptRows, err := s.listCatalogEntryPages()
	if err != nil {
		return nil, err
	}
	for _, r := range conceptRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	topicRows, err := s.listCatalogTopicPages()
	if err != nil {
		return nil, err
	}
	for _, r := range topicRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	candRows, err := s.listCatalogCandidates()
	if err != nil {
		return nil, err
	}
	for _, r := range candRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	wizardRows, err := s.listCatalogWizardTasks()
	if err != nil {
		return nil, err
	}
	for _, r := range wizardRows {
		byDomain[r.domainID] = append(byDomain[r.domainID], r)
	}

	out := make([]CatalogDomain, 0, len(domains))
	for _, d := range domains {
		rows := byDomain[d.DomainID]
		sortCatalogRows(rows)
		pages := make([]CatalogCard, 0, len(rows))
		wikiCount := 0
		for _, r := range rows {
			pages = append(pages, r.card)
			if r.card.Kind != CatalogKindWizardTask {
				wikiCount++
			}
		}
		d.WikiCount = wikiCount
		d.Pages = pages
		out = append(out, d)
	}
	return out, nil
}

// listCatalogWizardTasks surfaces the in-progress/errored 分步向导 task (at
// most one per domain, docs/impl/v1/wiki.md 步骤 8 "分步向导" 断点续开,
// 2026-08-07 新增) as a catalog card so it's findable after a reload or an
// accidental modal close — clicking it reopens the wizard at whatever step
// its status implies (frontend's job; this only surfaces the pointer).
func (s *Store) listCatalogWizardTasks() ([]catalogRow, error) {
	rows, err := s.db.Query(`SELECT task_id, domain_id, topic_name, status, error_message, candidates_json, updated_at
		FROM wiki_wizard_tasks`)
	if err != nil {
		return nil, fmt.Errorf("wiki store: catalog wizard tasks: %w", err)
	}
	defer rows.Close()

	var out []catalogRow
	for rows.Next() {
		var taskID, domainID, topicName, status, candidatesJSON string
		var errMsg sql.NullString
		var updatedAt time.Time
		if err := rows.Scan(&taskID, &domainID, &topicName, &status, &errMsg, &candidatesJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("wiki store: scan catalog wizard task: %w", err)
		}
		desc := ""
		switch status {
		case WizardTaskStatusCandidatesLoading:
			desc = "检索中，可能需要 30-60 秒"
		case WizardTaskStatusCandidatesReady:
			var entries []TopicCandidateEntry
			_ = json.Unmarshal([]byte(candidatesJSON), &entries)
			desc = fmt.Sprintf("%d 个候选词条待处理", len(entries))
		case WizardTaskStatusError:
			desc = errMsg.String
		}
		out = append(out, catalogRow{
			domainID:  domainID,
			updatedAt: updatedAt,
			card: CatalogCard{
				Kind: CatalogKindWizardTask, TaskID: taskID, Title: topicName,
				Description: desc, Status: status, UpdatedAt: updatedAt.Format(time.RFC3339),
			},
		})
	}
	return out, rows.Err()
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

// listCatalogEntryPages lists every published-or-not 词条页 (concept AND
// fact pages, not just kind=concept — topic pages are excluded and listed
// separately by listCatalogTopicPages) for the catalog's per-domain buckets.
func (s *Store) listCatalogEntryPages() ([]catalogRow, error) {
	rows, err := s.db.Query(`
		SELECT p.page_id, p.page_type, p.entry_id, p.title, p.status, p.summary,
			p.updated_at, c.domain_id, COALESCE(c.description, '')
		FROM wiki_pages p
		JOIN entries c ON c.entry_id = p.entry_id
		WHERE p.page_type != ?
		ORDER BY p.updated_at DESC`, PageTypeTopic)
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

func (s *Store) listCatalogTopicPages() ([]catalogRow, error) {
	// One row per (topic page, member domain). A topic spanning multiple
	// domains is intentionally duplicated across those domain buckets.
	rows, err := s.db.Query(`
		SELECT p.page_id, p.page_type, p.title, p.status, p.summary, p.updated_at,
			c.domain_id,
			GROUP_CONCAT(c.name, '、') AS member_names
		FROM wiki_pages p
		JOIN wiki_page_relations r
			ON r.from_page_id = p.page_id AND r.relation_type = ?
		JOIN wiki_pages mp ON mp.page_id = r.to_page_id
		JOIN entries c ON c.entry_id = mp.entry_id
		WHERE p.page_type = ?
		GROUP BY p.page_id, p.page_type, p.title, p.status, p.summary, p.updated_at, c.domain_id
		ORDER BY p.updated_at DESC`, RelationContains, PageTypeTopic)
	if err != nil {
		return nil, fmt.Errorf("wiki store: catalog topic pages: %w", err)
	}
	defer rows.Close()

	var out []catalogRow
	for rows.Next() {
		var (
			pageID, pageType, title, status, summary, domainID string
			memberNames                                        sql.NullString
			updatedAt                                          time.Time
		)
		if err := rows.Scan(&pageID, &pageType, &title, &status, &summary, &updatedAt,
			&domainID, &memberNames); err != nil {
			return nil, fmt.Errorf("wiki store: scan catalog topic page: %w", err)
		}
		desc := strings.TrimSpace(summary)
		if desc == "" && memberNames.Valid {
			desc = strings.TrimSpace(memberNames.String)
		}
		out = append(out, catalogRow{
			domainID:  domainID,
			updatedAt: updatedAt,
			card: CatalogCard{
				Kind:        CatalogKindPage,
				PageID:      pageID,
				PageType:    pageType,
				Title:       title,
				Description: desc,
				Status:      status,
				UpdatedAt:   updatedAt.Format(time.RFC3339),
			},
		})
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
