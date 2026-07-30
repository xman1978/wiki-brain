package wiki

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const pageColumns = `page_id, page_type, concept_id, title, content, status,
	source_point_ids, source_unit_ids, source_link_ids, observed_conditions, aliases, trigger_questions,
	member_roles, uncovered_points, compiled_from, prompt_version, model_name,
	compiled_at, published_at, created_at, updated_at`

func scanPage(row interface{ Scan(...interface{}) error }) (*Page, error) {
	var p Page
	err := row.Scan(&p.PageID, &p.PageType, &p.ConceptID, &p.Title, &p.Content, &p.Status,
		&p.SourcePointIDs, &p.SourceUnitIDs, &p.SourceLinkIDs, &p.ObservedConditions, &p.Aliases, &p.TriggerQuestions,
		&p.MemberRoles, &p.UncoveredPoints, &p.CompiledFrom, &p.PromptVersion, &p.ModelName,
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
	if p.Aliases == "" {
		p.Aliases = "[]"
	}
	if p.TriggerQuestions == "" {
		p.TriggerQuestions = "[]"
	}
	if p.ObservedConditions == "" {
		p.ObservedConditions = "[]"
	}
	if p.MemberRoles == "" {
		p.MemberRoles = "[]"
	}
	if p.UncoveredPoints == "" {
		p.UncoveredPoints = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, concept_id, title, content, status, source_point_ids, source_unit_ids,
		 source_link_ids, observed_conditions, aliases, trigger_questions, member_roles, uncovered_points,
		 compiled_from, prompt_version, model_name, compiled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		p.PageID, p.PageType, p.ConceptID, p.Title, p.Content, p.Status,
		p.SourcePointIDs, p.SourceUnitIDs, p.SourceLinkIDs, p.ObservedConditions, p.Aliases, p.TriggerQuestions,
		p.MemberRoles, p.UncoveredPoints, p.CompiledFrom, p.PromptVersion, p.ModelName)
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
func (s *Store) ReplaceContent(pageID, title, content, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, observedConditionsJSON, aliasesJSON, triggerQuestionsJSON, uncoveredPointsJSON, compiledFromJSON, promptVersion, modelName string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET
		title = ?, content = ?, status = ?, source_point_ids = ?, source_unit_ids = ?, source_link_ids = ?, observed_conditions = ?,
		aliases = ?, trigger_questions = ?, uncovered_points = ?,
		compiled_from = ?, prompt_version = ?, model_name = ?, compiled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE page_id = ?`,
		title, content, StatusDraft, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, observedConditionsJSON,
		aliasesJSON, triggerQuestionsJSON, uncoveredPointsJSON, compiledFromJSON, promptVersion, modelName, pageID)
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

// ListQualifyingPoints implements the qualifying-KP definition from
// docs/design/wiki-compilation.md "ActivationLink 回答'这条管不管用'，Wiki
// 编译回答'这个主题够不够格立传'": reliability is answered once, by the KP
// having a verified ActivationLink — no separate confident_count floor
// (re-checking a second count on top of verified would just re-ask the same
// question verified already answered). confident_count is still selected
// (MAX) and used to order results descending — the order compile input
// truncation relies on when over compile_max_chars, not a filter.
func (s *Store) ListQualifyingPoints(conceptID string) ([]QualifyingPoint, error) {
	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, ku.center, ku.line_start, ku.line_end,
			MAX(lc.confident_count) AS max_confident
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.concept_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			AND EXISTS (SELECT 1 FROM activation_links al WHERE al.point_id = kp.point_id AND al.status = 'verified')
		GROUP BY kp.point_id
		ORDER BY max_confident DESC`, conceptID)
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

// ListUncoveredPoints implements the uncovered_points field
// (docs/impl/v1/wiki.md "数据结构"): lifecycle=current KPs under conceptID
// that are NOT qualifying — no verified ActivationLink. Field-only output;
// never feeds the citation whitelist or any compile gate.
func (s *Store) ListUncoveredPoints(conceptID string) ([]UncoveredPoint, error) {
	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.content
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.concept_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			AND NOT EXISTS (SELECT 1 FROM activation_links al WHERE al.point_id = kp.point_id AND al.status = 'verified')`, conceptID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list uncovered points: %w", err)
	}
	defer rows.Close()

	var out []UncoveredPoint
	for rows.Next() {
		var u UncoveredPoint
		if err := rows.Scan(&u.PointID, &u.Summary); err != nil {
			return nil, fmt.Errorf("wiki store: scan uncovered point: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateMemberRoles overwrites a topic page's member_roles field in place.
func (s *Store) UpdateMemberRoles(pageID, memberRolesJSON string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET member_roles = ?, updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`,
		memberRolesJSON, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: update member roles: %w", err)
	}
	return nil
}

// UpdateUncoveredPoints overwrites a page's uncovered_points field in place —
// used by recompile paths that don't otherwise rewrite the whole row via
// ReplaceContent/ReplaceTopicContent.
func (s *Store) UpdateUncoveredPoints(pageID, uncoveredPointsJSON string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET uncovered_points = ?, updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`,
		uncoveredPointsJSON, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: update uncovered points: %w", err)
	}
	return nil
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

// VerifiedLinksObservedConditions unions the observed_conditions of every
// verified ActivationLink whose point_id is in pointIDs — becomes
// wiki_pages.observed_conditions at compile/recompile time (docs/design/
// wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组"). Dedup
// reuses activation.MergeObservedConditions's existing four-tuple key rather
// than reinventing one.
func (s *Store) VerifiedLinksObservedConditions(pointIDs []string) ([]activation.ObservedCondition, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT observed_conditions FROM activation_links WHERE status = 'verified' AND point_id IN (%s)`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: verified links observed conditions: %w", err)
	}
	defer rows.Close()

	var merged []activation.ObservedCondition
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("wiki store: scan observed conditions: %w", err)
		}
		if raw == "" {
			continue
		}
		var conds []activation.ObservedCondition
		if err := json.Unmarshal([]byte(raw), &conds); err != nil {
			return nil, fmt.Errorf("wiki store: decode observed conditions: %w", err)
		}
		for _, c := range conds {
			merged = activation.MergeObservedConditions(merged, c, 0)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return merged, nil
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

// ConfidentQuestionsForPoints fetches real confirmed question text for
// trigger_questions generation material (docs/design/wiki-compilation.md
// "触发问法取材真实观测，检索匹配复用四元组"): per point_id, the raw
// traces.question of every retrieval_quality='confident' trace that actually
// cited it (same json_each EXISTS pattern as study.Store.ConfidentTraceQuadruples,
// just selecting the question text instead of the four-tuple). Samples
// round-robin across pointIDs so no single KP's questions crowd out the
// others, dedups, and caps at limit.
func (s *Store) ConfidentQuestionsForPoints(pointIDs []string, limit int) ([]string, error) {
	if len(pointIDs) == 0 || limit <= 0 {
		return nil, nil
	}

	perPoint := make([][]string, len(pointIDs))
	for i, pid := range pointIDs {
		rows, err := s.db.Query(`
			SELECT DISTINCT t.question
			FROM traces t
			WHERE t.retrieval_quality = 'confident'
			  AND EXISTS (SELECT 1 FROM json_each(t.direct_point_ids) je WHERE je.value = ?)
			ORDER BY t.created_at`, pid)
		if err != nil {
			return nil, fmt.Errorf("wiki store: confident questions for point %s: %w", pid, err)
		}
		var qs []string
		for rows.Next() {
			var q string
			if err := rows.Scan(&q); err != nil {
				rows.Close()
				return nil, fmt.Errorf("wiki store: scan confident question: %w", err)
			}
			qs = append(qs, q)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return nil, rowsErr
		}
		perPoint[i] = qs
	}

	seen := make(map[string]bool)
	var out []string
	for i := 0; len(out) < limit; i++ {
		progressed := false
		for _, qs := range perPoint {
			if i >= len(qs) {
				continue
			}
			progressed = true
			q := qs[i]
			if seen[q] {
				continue
			}
			seen[q] = true
			out = append(out, q)
			if len(out) >= limit {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out, nil
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

// PublishedConceptPage is one (concept name, page_id) pair for a published
// page tied to a concept — the concept entry's candidate source
// (docs/impl/v1/wiki.md 步骤 4b, retrieval.md 第 0 层): the question is
// matched against concept names in Go (word-lexical contains, not a DB
// query) since it needs the shared foundation/text normalizer.
type PublishedConceptPage struct {
	ConceptID string
	Name      string
	PageID    string
}

// ListPublishedConceptPages backs the concept entry: every published page
// with a non-null concept_id, joined to its concept name.
func (s *Store) ListPublishedConceptPages() ([]PublishedConceptPage, error) {
	rows, err := s.db.Query(`
		SELECT c.concept_id, c.name, w.page_id
		FROM wiki_pages w
		JOIN concepts c ON c.concept_id = w.concept_id
		WHERE w.status = ?`, StatusPublished)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list published concept pages: %w", err)
	}
	defer rows.Close()

	var out []PublishedConceptPage
	for rows.Next() {
		var p PublishedConceptPage
		if err := rows.Scan(&p.ConceptID, &p.Name, &p.PageID); err != nil {
			return nil, fmt.Errorf("wiki store: scan published concept page: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PageConditions is one published page's aggregated observed_conditions —
// the four-tuple retrieval entry's candidate source (docs/impl/v1/wiki.md
// 步骤 4c).
type PageConditions struct {
	PageID     string
	Conditions []activation.ObservedCondition
}

// ListPublishedPagesWithConditions backs the four-tuple entry: every
// published page's decoded observed_conditions. Pages with an empty ('[]')
// or malformed observed_conditions are skipped rather than failing the whole
// list (this entry is best-effort — the lexical/concept entries remain
// available regardless).
func (s *Store) ListPublishedPagesWithConditions() ([]PageConditions, error) {
	rows, err := s.db.Query(`SELECT page_id, observed_conditions FROM wiki_pages WHERE status = ?`, StatusPublished)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list published pages with conditions: %w", err)
	}
	defer rows.Close()

	var out []PageConditions
	for rows.Next() {
		var pageID, raw string
		if err := rows.Scan(&pageID, &raw); err != nil {
			return nil, fmt.Errorf("wiki store: scan page conditions: %w", err)
		}
		if raw == "" || raw == "[]" {
			continue
		}
		var conds []activation.ObservedCondition
		if err := json.Unmarshal([]byte(raw), &conds); err != nil {
			continue
		}
		if len(conds) == 0 {
			continue
		}
		out = append(out, PageConditions{PageID: pageID, Conditions: conds})
	}
	return out, rows.Err()
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

// PointDetailsForIDs resolves point_ids to their KP content, owning KU, and
// source location — feeds wiki_drafts.evidence_index
// (docs/impl/v1/wiki.md 步骤 10).
func (s *Store) PointDetailsForIDs(pointIDs []string) ([]EvidenceIndexEntry, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT kp.point_id, kp.content, ku.unit_id, ku.center, ku.source_id, ku.line_start, ku.line_end
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.point_id IN (%s)`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: point details for ids: %w", err)
	}
	defer rows.Close()

	var out []EvidenceIndexEntry
	for rows.Next() {
		var e EvidenceIndexEntry
		if err := rows.Scan(&e.PointID, &e.PointSummary, &e.UnitID, &e.UnitTopic,
			&e.SourceRef.SourceID, &e.SourceRef.LineStart, &e.SourceRef.LineEnd); err != nil {
			return nil, fmt.Errorf("wiki store: scan point detail: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InsertDraft creates a wiki_drafts row.
func (s *Store) InsertDraft(d *Draft) error {
	if d.DraftID == "" {
		d.DraftID = uuid.New().String()
	}
	if d.SourcePageIDs == "" {
		d.SourcePageIDs = "[]"
	}
	if d.EvidenceIndex == "" {
		d.EvidenceIndex = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO wiki_drafts
		(draft_id, page_id, source_revision_id, source_page_ids, evidence_index, title, content, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DraftID, d.PageID, d.SourceRevisionID, d.SourcePageIDs, d.EvidenceIndex, d.Title, d.Content, d.Note)
	if err != nil {
		return fmt.Errorf("wiki store: insert draft: %w", err)
	}
	return nil
}

const draftColumns = `draft_id, page_id, source_revision_id, source_page_ids, evidence_index, title, content, note, created_at, updated_at`

func scanDraft(row interface{ Scan(...interface{}) error }) (*Draft, error) {
	var d Draft
	err := row.Scan(&d.DraftID, &d.PageID, &d.SourceRevisionID, &d.SourcePageIDs, &d.EvidenceIndex,
		&d.Title, &d.Content, &d.Note, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) GetDraft(draftID string) (*Draft, error) {
	row := s.db.QueryRow(`SELECT `+draftColumns+` FROM wiki_drafts WHERE draft_id = ?`, draftID)
	d, err := scanDraft(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get draft: %w", err)
	}
	return d, nil
}

func (s *Store) ListDrafts(pageID string, limit int) ([]Draft, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + draftColumns + ` FROM wiki_drafts WHERE 1=1`
	var args []interface{}
	if pageID != "" {
		query += ` AND page_id = ?`
		args = append(args, pageID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list drafts: %w", err)
	}
	defer rows.Close()

	var drafts []Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("wiki store: scan draft: %w", err)
		}
		drafts = append(drafts, *d)
	}
	return drafts, rows.Err()
}

func (s *Store) UpdateDraft(d *Draft) error {
	_, err := s.db.Exec(`UPDATE wiki_drafts SET title = ?, content = ?, note = ?, updated_at = CURRENT_TIMESTAMP WHERE draft_id = ?`,
		d.Title, d.Content, d.Note, d.DraftID)
	if err != nil {
		return fmt.Errorf("wiki store: update draft: %w", err)
	}
	return nil
}

func (s *Store) DeleteDraft(draftID string) error {
	_, err := s.db.Exec(`DELETE FROM wiki_drafts WHERE draft_id = ?`, draftID)
	if err != nil {
		return fmt.Errorf("wiki store: delete draft: %w", err)
	}
	return nil
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
