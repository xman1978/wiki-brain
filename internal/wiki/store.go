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

const pageColumns = `page_id, page_type, entry_id, title, content, status,
	source_point_ids, source_unit_ids, source_link_ids, observed_conditions, aliases, trigger_questions,
	member_roles, uncovered_points, compiled_from, summary, aspects, prompt_version, model_name,
	compiled_at, published_at, created_at, updated_at,
	synthesis_success_count, synthesis_failure_count, synthesis_audited_success_count, synthesis_audited_failure_count`

// pageColumnsPrefixed is pageColumns qualified with the "p" alias, for
// queries that JOIN wiki_pages against another table also named page_id/
// entry_id (e.g. wiki_page_entries) where the bare column list would be
// ambiguous.
const pageColumnsPrefixed = `p.page_id, p.page_type, p.entry_id, p.title, p.content, p.status,
	p.source_point_ids, p.source_unit_ids, p.source_link_ids, p.observed_conditions, p.aliases, p.trigger_questions,
	p.member_roles, p.uncovered_points, p.compiled_from, p.summary, p.aspects, p.prompt_version, p.model_name,
	p.compiled_at, p.published_at, p.created_at, p.updated_at,
	p.synthesis_success_count, p.synthesis_failure_count, p.synthesis_audited_success_count, p.synthesis_audited_failure_count`

func scanPage(row interface{ Scan(...interface{}) error }) (*Page, error) {
	var p Page
	err := row.Scan(&p.PageID, &p.PageType, &p.EntryID, &p.Title, &p.Content, &p.Status,
		&p.SourcePointIDs, &p.SourceUnitIDs, &p.SourceLinkIDs, &p.ObservedConditions, &p.Aliases, &p.TriggerQuestions,
		&p.MemberRoles, &p.UncoveredPoints, &p.CompiledFrom, &p.Summary, &p.Aspects, &p.PromptVersion, &p.ModelName,
		&p.CompiledAt, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt,
		&p.SynthesisSuccessCount, &p.SynthesisFailureCount, &p.SynthesisAuditedSuccessCount, &p.SynthesisAuditedFailureCount)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) InsertPage(p *Page) error {
	return s.InsertPageWithEntries(p, nil)
}

// InsertPageWithEntries is InsertPage plus, in the same transaction, writing
// the full entryIDs set into wiki_page_entries (docs/impl/v1/
// wiki-single-tier-open-questions.md「已拍板（2026-08-18）」第 1 条:
// wiki_pages.entry_id 单值列只存主 entry, wiki_page_entries 承载完整集合
// for Recompile / entry_id -> page_id lookups). entryIDs may be empty (some
// callers/tests insert pages without a compile request), in which case only
// wiki_pages is written.
func (s *Store) InsertPageWithEntries(p *Page, entryIDs []string) error {
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
	if p.Aspects == "" {
		p.Aspects = "[]"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("wiki store: insert page: begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, entry_id, title, content, status, source_point_ids, source_unit_ids,
		 source_link_ids, observed_conditions, aliases, trigger_questions, member_roles, uncovered_points,
		 compiled_from, summary, aspects, prompt_version, model_name, compiled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		p.PageID, p.PageType, p.EntryID, p.Title, p.Content, p.Status,
		p.SourcePointIDs, p.SourceUnitIDs, p.SourceLinkIDs, p.ObservedConditions, p.Aliases, p.TriggerQuestions,
		p.MemberRoles, p.UncoveredPoints, p.CompiledFrom, p.Summary, p.Aspects, p.PromptVersion, p.ModelName)
	if err != nil {
		return fmt.Errorf("wiki store: insert page: %w", err)
	}

	seen := make(map[string]bool, len(entryIDs))
	for _, entryID := range entryIDs {
		if entryID == "" || seen[entryID] {
			continue
		}
		seen[entryID] = true
		if _, err := tx.Exec(`INSERT INTO wiki_page_entries (page_id, entry_id) VALUES (?, ?)`,
			p.PageID, entryID); err != nil {
			return fmt.Errorf("wiki store: insert page entry: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("wiki store: insert page: commit: %w", err)
	}
	return nil
}

// EntryIDsByPageID returns the full entry_id set a page was compiled from,
// read from wiki_page_entries (docs/impl/v1/wiki-single-tier-open-questions.md
// 已拍板 第 1 条). Falls back to page.EntryID's caller when this returns
// empty (pages inserted before migration 057, or without entryIDs at all).
func (s *Store) EntryIDsByPageID(pageID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT entry_id FROM wiki_page_entries WHERE page_id = ?`, pageID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: entry ids by page id: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: entry ids by page id: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PageIDsByEntryID reverse-looks-up every page (any status) that entryID was
// compiled into, via wiki_page_entries — supports 步骤 4's entry_id -> page_id
// lookup (docs/impl/v1/wiki-single-tier-open-questions.md 已拍板 第 1 条).
// Not yet called by any production path in this task; wired for step 4.
func (s *Store) PageIDsByEntryID(entryID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT page_id FROM wiki_page_entries WHERE entry_id = ?`, entryID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: page ids by entry id: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: page ids by entry id: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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

// GetActivePageByEntryID finds a non-archived page whose entry set contains
// conceptID — used to reject a duplicate POST /wiki/compile (docs/impl/v1/
// wiki.md 步骤 2). Checks wiki_page_entries first (docs/impl/v1/
// wiki-single-tier-open-questions.md「已拍板（2026-08-18）」第 1 条: a page
// compiled from multiple entry_ids only has its FIRST one on wiki_pages.
// entry_id, so a duplicate-compile check against a non-primary entry_id must
// go through the relation table, not the single column), falling back to the
// single wiki_pages.entry_id column for pages inserted before migration 057
// or without any wiki_page_entries rows.
func (s *Store) GetActivePageByEntryID(conceptID string) (*Page, error) {
	row := s.db.QueryRow(`SELECT `+pageColumnsPrefixed+`
		FROM wiki_page_entries wpe
		JOIN wiki_pages p ON p.page_id = wpe.page_id
		WHERE wpe.entry_id = ? AND p.status != ? LIMIT 1`,
		conceptID, StatusArchived)
	p, err := scanPage(row)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("wiki store: get active page by concept (relation): %w", err)
	}

	row = s.db.QueryRow(`SELECT `+pageColumns+` FROM wiki_pages WHERE entry_id = ? AND status != ? LIMIT 1`,
		conceptID, StatusArchived)
	p, err = scanPage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get active page by concept: %w", err)
	}
	return p, nil
}

func (s *Store) ListPages(status, pageType, conceptID string, limit int) ([]Page, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + pageColumns + ` FROM wiki_pages WHERE 1 = 1`
	var args []interface{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if pageType != "" {
		query += ` AND page_type = ?`
		args = append(args, pageType)
	}
	if conceptID != "" {
		query += ` AND entry_id = ?`
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
	return s.ListPages(StatusPublished, "", "", 100000)
}

func (s *Store) UpdatePageStatus(pageID, status string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`, status, pageID)
	if err != nil {
		return fmt.Errorf("wiki store: update page status: %w", err)
	}
	return nil
}

// RecordSynthesisOutcome updates a page's synthesis-satisfaction axis
// (docs/impl/v1/wiki.md 步骤 4a) after one independent-verification trial.
// Audit-only by design (unlike ActivationLink/Bundle's success_count/
// audited_success_count split): every data point on this axis comes from
// the same independent-comparison mechanism, so success_count and
// audited_success_count (or the failure pair) always advance together —
// there is no separate self-graded tier a served-but-unaudited answer could
// contribute to (wiki.md 步骤 4a「未中选」explicitly rules that out).
// Deliberately does NOT touch status/needs_recompile/index — the synthesis
// axis is observation-only (wiki.md 步骤 4a「mean(page) 的消费方式」).
func (s *Store) RecordSynthesisOutcome(pageID string, agree bool) error {
	var q string
	if agree {
		q = `UPDATE wiki_pages SET synthesis_success_count = synthesis_success_count + 1,
			synthesis_audited_success_count = synthesis_audited_success_count + 1,
			updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`
	} else {
		q = `UPDATE wiki_pages SET synthesis_failure_count = synthesis_failure_count + 1,
			synthesis_audited_failure_count = synthesis_audited_failure_count + 1,
			updated_at = CURRENT_TIMESTAMP WHERE page_id = ?`
	}
	if _, err := s.db.Exec(q, pageID); err != nil {
		return fmt.Errorf("wiki store: record synthesis outcome: %w", err)
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
func (s *Store) ReplaceContent(pageID, title, content, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, observedConditionsJSON, aliasesJSON, triggerQuestionsJSON, uncoveredPointsJSON, compiledFromJSON, summary, aspectsJSON, promptVersion, modelName string) error {
	_, err := s.db.Exec(`UPDATE wiki_pages SET
		title = ?, content = ?, status = ?, source_point_ids = ?, source_unit_ids = ?, source_link_ids = ?, observed_conditions = ?,
		aliases = ?, trigger_questions = ?, uncovered_points = ?,
		compiled_from = ?, summary = ?, aspects = ?, prompt_version = ?, model_name = ?, compiled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE page_id = ?`,
		title, content, StatusDraft, sourcePointIDsJSON, sourceUnitIDsJSON, sourceLinkIDsJSON, observedConditionsJSON,
		aliasesJSON, triggerQuestionsJSON, uncoveredPointsJSON, compiledFromJSON, summary, aspectsJSON, promptVersion, modelName, pageID)
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

// LatestRevisionID returns the most recently inserted wiki_revisions row for
// pageID — used by the pre-publish quality gate to know which revision its
// claim checks and self-check results should be filed under
// (docs/impl/v1/wiki-generation.md 阶段 E/G). Returns "" with no error if the
// page has no revisions yet.
func (s *Store) LatestRevisionID(pageID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT revision_id FROM wiki_revisions WHERE page_id = ? ORDER BY created_at DESC LIMIT 1`, pageID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("wiki store: latest revision id: %w", err)
	}
	return id, nil
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
// 编译回答'这个主题够不够格立传'": for the Study-recommended path
// (requireVerified=true) reliability is answered once, by the KP having a
// verified ActivationLink — no separate confident_count floor (re-checking a
// second count on top of verified would just re-ask the same question
// verified already answered).
//
// requireVerified=false (2026-08-07 修订, docs/impl/v1/wiki.md 步骤 2
// "人工指定主题手动编译"): the manual-trigger path (result_id=="") drops the
// verified requirement — qualifying is just lifecycle=current + entry
// membership, same scope as 步骤 8's topic-scope material — so material with
// no real-usage history yet can still reach a draft page. Callers thread
// requireVerified through from whether the triggering request carried a
// Study result_id; it does not affect isEntryReady's own readiness signal
// (still verified-gated) or Publish's selfcheck quality gate.
//
// confident_count is still selected (MAX, 0 when the KP was never matched as
// a link candidate) and used to order results descending — the order compile
// input truncation relies on when over compile_max_chars, not a filter.
func (s *Store) ListQualifyingPoints(conceptID string, requireVerified bool) ([]QualifyingPoint, error) {
	query := `
		SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, ku.center, ku.line_start, ku.line_end,
			COALESCE(MAX(lc.confident_count), 0) AS max_confident
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		LEFT JOIN link_candidates lc ON lc.point_id = kp.point_id
		WHERE ku.entry_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'`
	if requireVerified {
		query += ` AND EXISTS (SELECT 1 FROM activation_links al WHERE al.point_id = kp.point_id AND al.status = 'verified')`
	}
	query += `
		GROUP BY kp.point_id
		ORDER BY max_confident DESC`
	rows, err := s.db.Query(query, conceptID)
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

// PointsByIDs resolves an arbitrary point_id list to QualifyingPoint-shaped
// content (lifecycle=current only), for buildKnowledgeSubgraph's Context/
// Conflict material (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3) —
// unlike ListQualifyingPoints, this isn't scoped to one entry_id (a related/
// contradicts one-hop can land on a KP under a different entry).
func (s *Store) PointsByIDs(pointIDs []string) ([]QualifyingPoint, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, ku.center, ku.line_start, ku.line_end,
			COALESCE(MAX(lc.confident_count), 0) AS max_confident
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		LEFT JOIN link_candidates lc ON lc.point_id = kp.point_id
		WHERE kp.point_id IN (%s) AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		GROUP BY kp.point_id`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: points by ids: %w", err)
	}
	defer rows.Close()

	var points []QualifyingPoint
	for rows.Next() {
		var p QualifyingPoint
		if err := rows.Scan(&p.PointID, &p.UnitID, &p.SourceID, &p.Content, &p.UnitCenter,
			&p.LineStart, &p.LineEnd, &p.ConfidentCount); err != nil {
			return nil, fmt.Errorf("wiki store: scan point: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// RelationsFromPoints batches the one-hop KPN expansion buildKnowledgeSubgraph
// needs for Context/Conflict (docs/impl/v1/wiki-single-tier-task-brief.md 步骤
// 3): every knowledge_point_relations row of relationType touching any of
// pointIDs on either side, in one IN-query rather than one query per Core KP
// (task brief explicitly warns against an N+1 loop here).
func (s *Store) RelationsFromPoints(pointIDs []string, relationType string) ([]PointRelation, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	allArgs := append([]interface{}{relationType}, args...)
	allArgs = append(allArgs, args...)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT source_point_id, target_point_id, relation_type
		FROM knowledge_point_relations
		WHERE relation_type = ? AND (source_point_id IN (%s) OR target_point_id IN (%s))`, ph, ph), allArgs...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: relations from points: %w", err)
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

// KPNConnectionCountsByType counts related/contradicts knowledge_point_relations
// rows among pointIDs — used by computeReadiness (docs/impl/v1/wiki.md 步骤 2
// "人工指定主题手动编译") for its informational readiness snapshot.
// Deliberately the same query as study/store.go's KPNConnectionCountsByType
// (intentionally parallel, not accidentally diverged — wiki can't import
// study, since study already imports wiki, see docs/impl/v1/wiki.md 步骤 2
// for why this is a small acceptable duplication rather than a shared helper).
func (s *Store) KPNConnectionCountsByType(pointIDs []string) (related, contradicts int, err error) {
	if len(pointIDs) < 2 {
		return 0, 0, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(pointIDs)*2)
	for i, id := range pointIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	args2 := make([]interface{}, len(args))
	copy(args2, args)
	allArgs := append(args, args2...)

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT relation_type, COUNT(*) FROM knowledge_point_relations
		WHERE source_point_id IN (%s) AND target_point_id IN (%s)
		GROUP BY relation_type`,
		placeholders, placeholders), allArgs...)
	if err != nil {
		return 0, 0, fmt.Errorf("wiki store: kpn connection counts by type: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var relType string
		var count int
		if err := rows.Scan(&relType, &count); err != nil {
			return 0, 0, fmt.Errorf("wiki store: scan kpn connection count: %w", err)
		}
		switch relType {
		case "related":
			related = count
		case "contradicts":
			contradicts = count
		}
	}
	return related, contradicts, rows.Err()
}

// DaysActive counts distinct days pointIDs were seen in question_kp_cooccurrence
// — same query as study/store.go's DaysActive, see KPNConnectionCountsByType's
// doc comment for why this is duplicated rather than shared.
func (s *Store) DaysActive(pointIDs []string) (int, error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(pointIDs))
	for i, id := range pointIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}

	var count int
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(DISTINCT DATE(last_seen_at)) FROM question_kp_cooccurrence
		WHERE point_id IN (%s)`, placeholders), args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("wiki store: days active: %w", err)
	}
	return count, nil
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
		WHERE ku.entry_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
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

// EntryAliases looks up real, already-verified synonym terms for a concept
// name from subject_synonyms (docs/impl/v1/activation.md "附属表
// subject_synonyms") — preset aliases and human-confirmed gap-mined ones are
// both status='active' rows in the same table, so one query covers both.
// Replaces the old LLM-generated aliases field
// (docs/design/wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组"
// 生成侧 修订: aliases 不应由 LLM 生成，而应直接取自系统里已经存在的真实数据).
func (s *Store) EntryAliases(conceptName string) ([]string, error) {
	if strings.TrimSpace(conceptName) == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT term FROM subject_synonyms WHERE canonical = ? AND status = 'active' ORDER BY term`, conceptName)
	if err != nil {
		return nil, fmt.Errorf("wiki store: concept aliases: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, fmt.Errorf("wiki store: scan concept alias: %w", err)
		}
		out = append(out, term)
	}
	return out, rows.Err()
}

// InsertClaimCheck writes one support-verdict row (docs/impl/v1/
// wiki-generation.md 阶段 E). CheckID is generated if empty.
func (s *Store) InsertClaimCheck(c *ClaimCheck) error {
	if c.CheckID == "" {
		c.CheckID = uuid.New().String()
	}
	if c.CitedPointIDs == "" {
		c.CitedPointIDs = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO wiki_claim_checks
		(check_id, page_id, revision_id, claim_id, claim_text, cited_point_ids, verdict, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CheckID, c.PageID, c.RevisionID, c.ClaimID, c.ClaimText, c.CitedPointIDs, c.Verdict, c.Reason)
	if err != nil {
		return fmt.Errorf("wiki store: insert claim check: %w", err)
	}
	return nil
}

// UnsupportedClaimCount counts verdict='unsupported' wiki_claim_checks rows
// for (pageID, revisionID) — the publish-blocking condition
// (docs/impl/v1/wiki-generation.md 阶段 E "处置").
func (s *Store) UnsupportedClaimCount(pageID, revisionID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM wiki_claim_checks WHERE page_id = ? AND revision_id = ? AND verdict = ?`,
		pageID, revisionID, VerdictUnsupported).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("wiki store: unsupported claim count: %w", err)
	}
	return n, nil
}

// ListClaimChecks returns every claim check for (pageID, revisionID), in
// insertion order — feeds the page detail view (docs/impl/v1/
// wiki-generation.md 11 "HTTP API 变更").
func (s *Store) ListClaimChecks(pageID, revisionID string) ([]ClaimCheck, error) {
	rows, err := s.db.Query(`SELECT check_id, page_id, revision_id, claim_id, claim_text, cited_point_ids, verdict, reason, created_at
		FROM wiki_claim_checks WHERE page_id = ? AND revision_id = ? ORDER BY created_at ASC`, pageID, revisionID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list claim checks: %w", err)
	}
	defer rows.Close()

	var out []ClaimCheck
	for rows.Next() {
		var c ClaimCheck
		if err := rows.Scan(&c.CheckID, &c.PageID, &c.RevisionID, &c.ClaimID, &c.ClaimText, &c.CitedPointIDs, &c.Verdict, &c.Reason, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("wiki store: scan claim check: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertQualityCheck writes one pre-publish self-check result
// (docs/impl/v1/wiki-generation.md 阶段 G). QCID is generated if empty.
func (s *Store) InsertQualityCheck(q *QualityCheck) error {
	if q.QCID == "" {
		q.QCID = uuid.New().String()
	}
	if q.Metrics == "" {
		q.Metrics = "{}"
	}
	_, err := s.db.Exec(`INSERT INTO wiki_quality_checks (qc_id, page_id, revision_id, metrics, passed, forced)
		VALUES (?, ?, ?, ?, ?, ?)`,
		q.QCID, q.PageID, q.RevisionID, q.Metrics, boolToInt(q.Passed), boolToInt(q.Forced))
	if err != nil {
		return fmt.Errorf("wiki store: insert quality check: %w", err)
	}
	return nil
}

// LatestQualityCheck returns the most recent wiki_quality_checks row for
// (pageID, revisionID), or nil if none exists yet — lets Publish reuse an
// already-computed self-check instead of replaying LLM calls a second time
// for the same revision (docs/impl/v1/wiki-generation.md 阶段 G "与 publish
// 的关系": "同一 revision 重复 publish 不重跑回放").
func (s *Store) LatestQualityCheck(pageID, revisionID string) (*QualityCheck, error) {
	var q QualityCheck
	var passed, forced int
	err := s.db.QueryRow(`SELECT qc_id, page_id, revision_id, metrics, passed, forced, created_at
		FROM wiki_quality_checks WHERE page_id = ? AND revision_id = ? ORDER BY created_at DESC LIMIT 1`,
		pageID, revisionID).Scan(&q.QCID, &q.PageID, &q.RevisionID, &q.Metrics, &passed, &forced, &q.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: latest quality check: %w", err)
	}
	q.Passed = passed != 0
	q.Forced = forced != 0
	return &q, nil
}

// MarkQualityCheckForced flips an existing wiki_quality_checks row to
// passed=1/forced=1 in place — used when a human overrides a failed
// pre-publish gate (docs/impl/v1/wiki-generation.md 阶段 G). Updating in
// place (rather than inserting a second row) keeps LatestQualityCheck's
// "most recent row" lookup unambiguous even when both events land in the
// same SQLite CURRENT_TIMESTAMP second.
func (s *Store) MarkQualityCheckForced(qcID string) error {
	_, err := s.db.Exec(`UPDATE wiki_quality_checks SET passed = 1, forced = 1 WHERE qc_id = ?`, qcID)
	if err != nil {
		return fmt.Errorf("wiki store: mark quality check forced: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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

// PublishedEntryPage is one (concept name, page_id) pair for a published
// page tied to a concept — the concept entry's candidate source
// (docs/impl/v1/wiki.md 步骤 4b, retrieval.md 第 0 层): the question is
// matched against concept names in Go (word-lexical contains, not a DB
// query) since it needs the shared foundation/text normalizer.
type PublishedEntryPage struct {
	EntryID string
	Name    string
	PageID  string
}

// ListPublishedEntryPages backs the concept entry: every published page
// with a non-null entry_id, joined to its concept name.
func (s *Store) ListPublishedEntryPages() ([]PublishedEntryPage, error) {
	rows, err := s.db.Query(`
		SELECT c.entry_id, c.name, w.page_id
		FROM wiki_pages w
		JOIN entries c ON c.entry_id = w.entry_id
		WHERE w.status = ?`, StatusPublished)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list published concept pages: %w", err)
	}
	defer rows.Close()

	var out []PublishedEntryPage
	for rows.Next() {
		var p PublishedEntryPage
		if err := rows.Scan(&p.EntryID, &p.Name, &p.PageID); err != nil {
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

// EntryCandidate is one entries row surfaced as a candidate for
// matchEntriesByConceptRecognition (docs/impl/v1/wiki-single-tier-task-brief.md
// 步骤 4) — name/description/boundary are exactly the disambiguation fields
// unit_entry_match.md already renders into its entry_list, reused here for
// the same purpose against a different consumer (a question instead of a
// knowledge unit).
type EntryCandidate struct {
	EntryID     string
	DomainID    string
	Name        string
	Description string
	Boundary    string
}

// ListEntriesForRecognition lists candidate entries for
// matchEntriesByConceptRecognition, excluding merged_into non-NULL rows (a
// merged concept is no longer a valid match target, mirroring
// unit.Store.GetEntriesByDomainID's same exclusion). domainIDs empty means
// no domain scoping — every entry across every domain is a candidate, same
// fallback shape as unit.Store.GetEntriesByDomainID("") for an unresolved
// domain.
func (s *Store) ListEntriesForRecognition(domainIDs []string) ([]EntryCandidate, error) {
	const cols = `entry_id, domain_id, name, description, boundary`
	var rows *sql.Rows
	var err error
	if len(domainIDs) > 0 {
		ph, args := buildPlaceholders(domainIDs)
		rows, err = s.db.Query(`SELECT `+cols+` FROM entries WHERE merged_into IS NULL AND domain_id IN (`+ph+`) ORDER BY name`, args...)
	} else {
		rows, err = s.db.Query(`SELECT ` + cols + ` FROM entries WHERE merged_into IS NULL ORDER BY name`)
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: list entries for recognition: %w", err)
	}
	defer rows.Close()

	var out []EntryCandidate
	for rows.Next() {
		var e EntryCandidate
		var desc, boundary sql.NullString
		if err := rows.Scan(&e.EntryID, &e.DomainID, &e.Name, &desc, &boundary); err != nil {
			return nil, fmt.Errorf("wiki store: scan entry candidate: %w", err)
		}
		if desc.Valid {
			e.Description = desc.String
		}
		if boundary.Valid {
			e.Boundary = boundary.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEntryInfo also returns kind (concept/fact, docs/impl/v1/kpn.md 步骤
// 3 "类型标注") and parentEntryID (entries.parent_entry_id,
// docs/impl/v1/fact-entry-parent-concept-task-brief.md): kind nudges the
// analyze/compile prompts' writing register via {{entry_kind_hint}} and
// determines the compiled page's page_type (pageTypeForKind); parentEntryID
// feeds buildKnowledgeSubgraph's Core-expansion rule for kind=fact entries
// (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3). Neither affects
// citation whitelist or relation-derivation logic, which are identical
// regardless of kind/parent.
func (s *Store) GetEntryInfo(conceptID string) (name, description, domainID, kind, parentEntryID string, err error) {
	var desc, dom, parent sql.NullString
	err = s.db.QueryRow(`SELECT name, description, domain_id, kind, parent_entry_id FROM entries WHERE entry_id = ?`, conceptID).
		Scan(&name, &desc, &dom, &kind, &parent)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("wiki store: get concept info: %w", err)
	}
	return name, desc.String, dom.String, kind, parent.String, nil
}

// GetDomainName resolves a domain_id to its display name for the outline
// prompt's {{domain_name}} var (docs/impl/v1/wiki-generation.md 3.3). Returns
// "" (not an error) if domainID is empty or not found — the outline stage
// treats a missing domain name as cosmetic, not fatal.
func (s *Store) GetDomainName(domainID string) (string, error) {
	if domainID == "" {
		return "", nil
	}
	var name string
	err := s.db.QueryRow(`SELECT name FROM domains WHERE domain_id = ?`, domainID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("wiki store: get domain name: %w", err)
	}
	return name, nil
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
