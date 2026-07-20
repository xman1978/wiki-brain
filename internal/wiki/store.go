package wiki

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const pageColumns = `page_id, page_type, concept_id, title, content, status,
	source_point_ids, source_unit_ids, source_link_ids, compiled_from, prompt_version, model_name,
	compiled_at, published_at, created_at, updated_at`

func scanPage(row interface{ Scan(...interface{}) error }) (*Page, error) {
	var p Page
	err := row.Scan(&p.PageID, &p.PageType, &p.ConceptID, &p.Title, &p.Content, &p.Status,
		&p.SourcePointIDs, &p.SourceUnitIDs, &p.SourceLinkIDs, &p.CompiledFrom, &p.PromptVersion, &p.ModelName,
		&p.CompiledAt, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) InsertPage(p *Page) error {
	if p.PageID == "" {
		p.PageID = uuid.New().String()
	}
	if p.Status == "" {
		p.Status = StatusDraft
	}
	_, err := s.db.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, concept_id, title, content, status, source_point_ids, source_unit_ids,
		 source_link_ids, compiled_from, prompt_version, model_name, compiled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		p.PageID, p.PageType, p.ConceptID, p.Title, p.Content, p.Status,
		p.SourcePointIDs, p.SourceUnitIDs, p.SourceLinkIDs, p.CompiledFrom, p.PromptVersion, p.ModelName)
	if err != nil {
		return fmt.Errorf("wiki store: insert page: %w", err)
	}
	return nil
}

func (s *Store) GetPage(pageID string) (*Page, error) {
	row := s.db.QueryRow(`SELECT `+pageColumns+` FROM wiki_pages WHERE page_id = ?`, pageID)
	p, err := scanPage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get page: %w", err)
	}
	return p, nil
}

// GetActivePageByConceptID finds a non-archived page for conceptID — used to
// reject a duplicate POST /wiki/compile (docs/impl/v1/wiki.md 步骤 2).
func (s *Store) GetActivePageByConceptID(conceptID string) (*Page, error) {
	row := s.db.QueryRow(`SELECT `+pageColumns+` FROM wiki_pages WHERE concept_id = ? AND status != ? LIMIT 1`,
		conceptID, StatusArchived)
	p, err := scanPage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get active page by concept: %w", err)
	}
	return p, nil
}

func (s *Store) ListPages(status, conceptID string, limit int) ([]Page, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + pageColumns + ` FROM wiki_pages WHERE 1 = 1`
	var args []interface{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if conceptID != "" {
		query += ` AND concept_id = ?`
		args = append(args, conceptID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list pages: %w", err)
	}
	defer rows.Close()

	var pages []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("wiki store: scan page: %w", err)
		}
		pages = append(pages, *p)
	}
	return pages, rows.Err()
}

// ListPublishedPages is used both by the lifecycle-triggered recompile scan
// and Study's periodic recompile scan (docs/impl/v1/wiki.md 步骤 5).
func (s *Store) ListPublishedPages() ([]Page, error) {
	return s.ListPages(StatusPublished, "", 100000)
}

func (s *Store) UpdatePageStatus(pageID, status string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`, status, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: update page status: %w", err)
	}
	return nil
}

func (s *Store) PublishPage(pageID string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET status = ?, published_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`,
		StatusPublished, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: publish page: %w", err)
	}
	return nil
}

// ReplaceContent overwrites a page's compiled content (used by both the
// initial compile and recompile — recompile just re-runs this on an existing
// page_id) and resets it to draft, ready to be published again.
func (s *Store) ReplaceContent(pageID, title, content, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, compiledFromJSON, promptVersion, modelName string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET
		title = ?, content = ?, status = ?, source_point_ids = ?, source_unit_ids = ?, source_link_ids = ?,
		compiled_from = ?, prompt_version = ?, model_name = ?, compiled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE page_id = ?`,
		title, content, StatusDraft, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, compiledFromJSON, promptVersion, modelName, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: replace content: %w", err)
	}
	return nil
}

func (s *Store) InsertRevision(r *Revision) error {
	if r.RevisionID == "" {
		r.RevisionID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO wiki_revisions (revision_id, page_id, content, reason) VALUES (?, ?, ?, ?)`,
		r.RevisionID, r.PageID, r.Content, r.Reason)
	if err != nil {
		return fmt.Errorf("wiki store: insert revision: %w", err)
	}
	return nil
}

func (s *Store) ListRevisions(pageID string) ([]Revision, error) {
	rows, err := s.db.Query(`SELECT revision_id, page_id, content, reason, created_at
		FROM wiki_revisions WHERE page_id = ? ORDER BY created_at ASC`, pageID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list revisions: %w", err)
	}
	defer rows.Close()

	var revs []Revision
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.RevisionID, &r.PageID, &r.Content, &r.Reason, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("wiki store: scan revision: %w", err)
		}
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

func (s *Store) GetRevision(pageID, revisionID string) (*Revision, error) {
	var r Revision
	err := s.db.QueryRow(`SELECT revision_id, page_id, content, reason, created_at
		FROM wiki_revisions WHERE page_id = ? AND revision_id = ?`, pageID, revisionID).
		Scan(&r.RevisionID, &r.PageID, &r.Content, &r.Reason, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get revision: %w", err)
	}
	return &r, nil
}

// ListQualifyingPoints implements docs/impl/v1/wiki.md 步骤 3's "qualifying
// KP": the same link_candidates-backed confident_count bar Study uses
// ("与 Study 候选口径一致"), scoped to one concept, lifecycle=current only.
// Ranked by confident_count descending — the order compile input truncation
// relies on when over compile_max_chars.
func (s *Store) ListQualifyingPoints(conceptID string, minConfident int) ([]QualifyingPoint, error) {
	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, ku.center, ku.line_start, ku.line_end,
			MAX(lc.confident_count) AS max_confident
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.concept_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		GROUP BY kp.point_id
		HAVING MAX(lc.confident_count) >= ?
		ORDER BY max_confident DESC`, conceptID, minConfident)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list qualifying points: %w", err)
	}
	defer rows.Close()

	var points []QualifyingPoint
	for rows.Next() {
		var p QualifyingPoint
		if err := rows.Scan(&p.PointID, &p.UnitID, &p.SourceID, &p.Content, &p.UnitCenter,
			&p.LineStart, &p.LineEnd, &p.ConfidentCount); err != nil {
			return nil, fmt.Errorf("wiki store: scan qualifying point: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// CountQualifyingPoints is the lightweight form Study's recompile scan
// (docs/impl/v1/wiki.md 步骤 5b) uses to compare against a page's
// source_point_ids count at compile time.
func (s *Store) CountQualifyingPoints(conceptID string, minConfident int) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT kp.point_id
			FROM link_candidates lc
			JOIN knowledge_points kp ON lc.point_id = kp.point_id
			JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
			WHERE ku.concept_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			GROUP BY kp.point_id
			HAVING MAX(lc.confident_count) >= ?
		)`, conceptID, minConfident).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("wiki store: count qualifying points: %w", err)
	}
	return count, nil
}

// RelationsAmong returns knowledge_point_relations rows whose both ends are
// in pointIDs (docs/impl/v1/wiki.md 步骤 3, "KPN 关系" compile input) —
// includes cross-Source relations, no scope filter.
func (s *Store) RelationsAmong(pointIDs []string) ([]PointRelation, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	allArgs := append(args, args...)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT source_point_id, target_point_id, relation_type
		FROM knowledge_point_relations
		WHERE source_point_id IN (%s) AND target_point_id IN (%s)`, ph, ph), allArgs...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: relations among: %w", err)
	}
	defer rows.Close()

	var rels []PointRelation
	for rows.Next() {
		var r PointRelation
		if err := rows.Scan(&r.SourcePointID, &r.TargetPointID, &r.RelationType); err != nil {
			return nil, fmt.Errorf("wiki store: scan relation: %w", err)
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

// VerifiedLinkIDsForPoints returns the activation_links.link_id of every
// verified link whose point_id is in pointIDs — the compile-time snapshot of
// "which ActivationLinks already cover this page's cited KPs", stored as
// wiki_pages.source_link_ids alongside source_point_ids/source_unit_ids
// (docs/impl/v1/wiki.md 步骤 3 扩展). point_id is unique per link, so this is
// at most len(pointIDs) rows.
func (s *Store) VerifiedLinkIDsForPoints(pointIDs []string) ([]string, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT link_id FROM activation_links WHERE status = 'verified' AND point_id IN (%s)`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: verified link ids for points: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan verified link id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type PointRelation struct {
	SourcePointID string
	TargetPointID string
	RelationType  string
}

// TopKnowledgeGaps feeds the term-overlap gap-matching step
// (docs/impl/v1/wiki.md 步骤 3); overlap itself is computed in Go
// (see service.go) since it needs the shared foundation/text tokenizer.
func (s *Store) TopKnowledgeGaps(limit int) ([]GapCandidate, error) {
	rows, err := s.db.Query(`SELECT question_terms, question, hit_count FROM knowledge_gaps ORDER BY hit_count DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("wiki store: top knowledge gaps: %w", err)
	}
	defer rows.Close()

	var gaps []GapCandidate
	for rows.Next() {
		var g GapCandidate
		if err := rows.Scan(&g.QuestionTerms, &g.Question, &g.HitCount); err != nil {
			return nil, fmt.Errorf("wiki store: scan gap: %w", err)
		}
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}

func (s *Store) GetSourceMarkdownPath(sourceID string) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT markdown_path FROM sources WHERE source_id = ?`, sourceID).Scan(&path)
	if err != nil {
		return "", fmt.Errorf("wiki store: get markdown path: %w", err)
	}
	return path, nil
}

func (s *Store) GetSourceTitle(sourceID string) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM sources WHERE source_id = ?`, sourceID).Scan(&title)
	if err != nil {
		return "", fmt.Errorf("wiki store: get source title: %w", err)
	}
	return title, nil
}

func (s *Store) GetConceptInfo(conceptID string) (name, description, domainID string, err error) {
	var desc, dom sql.NullString
	err = s.db.QueryRow(`SELECT name, description, domain_id FROM concepts WHERE concept_id = ?`, conceptID).
		Scan(&name, &desc, &dom)
	if err != nil {
		return "", "", "", fmt.Errorf("wiki store: get concept info: %w", err)
	}
	return name, desc.String, dom.String, nil
}

func buildPlaceholders(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
