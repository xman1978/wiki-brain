// Store queries backing topic-page candidate range detection
// (docs/impl/v1/wiki.md 步骤 8, 2026-08-03 修订).
package wiki

import (
	"database/sql"
	"fmt"
	"time"
)

// QualifyingPointRef is the minimal shape topic-candidate detection needs
// out of the candidate-range KP retrieval: enough to group by entry_id
// (步骤 8 第 5 步) without pulling full QualifyingPoint content.
type QualifyingPointRef struct {
	PointID string
	EntryID string
}

// QualifyingPointsByIDs filters an arbitrary point_id list down to KPs that
// are usable as *topic-scope material* (docs/impl/v1/wiki.md 步骤 8 第 4 步,
// 2026-08-04 修订): lifecycle=current on both KP and KU. Verified
// ActivationLink is intentionally NOT required here — topic-scope retrieval
// (manual POST /wiki/topics and Study DetectTopicCandidate) only decides
// "which current KPs fall in this theme", so a draft wiki can be assembled
// before usage has verified those links. Formalization still depends on
// verified signals: first-tier compile material (ListQualifyingPoints),
// entry-level ready, second-tier reliability, and publish (unless force).
//
// Naming note: historically this shared the "qualifying" label with
// ListQualifyingPoints (current + verified). Topic scope and one-tier
// compile now diverge on the verified leg; callers must not treat the two
// as interchangeable.
func (s *Store) QualifyingPointsByIDs(pointIDs []string) ([]QualifyingPointRef, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT kp.point_id, ku.entry_id
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.point_id IN (%s) AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			AND ku.entry_id IS NOT NULL`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: qualifying points by ids: %w", err)
	}
	defer rows.Close()

	var out []QualifyingPointRef
	for rows.Next() {
		var r QualifyingPointRef
		if err := rows.Scan(&r.PointID, &r.EntryID); err != nil {
			return nil, fmt.Errorf("wiki store: scan qualifying point ref: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasPendingWikiCandidate checks for an already-pending wiki_candidate
// learning_result for conceptID, so 步骤 6's "随批推进" member creation
// doesn't duplicate what Study's own flagWikiCandidates cycle may already
// have written.
func (s *Store) HasPendingWikiCandidate(conceptID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM learning_results
		WHERE action = 'wiki_candidate' AND object_type = 'wiki_page' AND object_id = ? AND status = 'pending_confirm' LIMIT 1`,
		conceptID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("wiki store: has pending wiki candidate: %w", err)
	}
	return true, nil
}

// PointDomainID resolves a KP's domain via its concept, for
// CreateTopicManual's optional domain_id scoping.
func (s *Store) PointDomainID(pointID string) (string, error) {
	var domainID string
	err := s.db.QueryRow(`
		SELECT c.domain_id FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN entries c ON ku.entry_id = c.entry_id
		WHERE kp.point_id = ?`, pointID).Scan(&domainID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("wiki store: point domain id: %w", err)
	}
	return domainID, nil
}

// VerifiedFraction implements 步骤 8 第 7 步"整体可靠度": the fraction of
// pointIDs (the full candidate-range retrieval result, not just the
// qualifying subset) that carry a verified ActivationLink.
func (s *Store) VerifiedFraction(pointIDs []string) (float64, error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	var verified int
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(DISTINCT point_id) FROM activation_links
		WHERE status = 'verified' AND point_id IN (%s)`, ph), args...).Scan(&verified)
	if err != nil {
		return 0, fmt.Errorf("wiki store: verified fraction: %w", err)
	}
	return float64(verified) / float64(len(pointIDs)), nil
}

// ChildOutlineIDs expands a set of outline node ids (from an outlines-index
// bleve hit) to include their descendant nodes within the same source
// (docs/impl/v1/wiki.md 步骤 8 "人工手动指定主题" 候选检索 1b, 2026-08-07
// 新增) — mirrors retrieval.Store.GetChildOutlineIDs's in-memory tree-build,
// duplicated rather than cross-package-imported per the existing
// KPNConnectionCountsByType precedent (wiki can't import retrieval any more
// than it can import study).
func (s *Store) ChildOutlineIDs(outlineIDs []string, sourceID string) ([]string, error) {
	if len(outlineIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT outline_id, parent_id FROM source_outlines WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: get child outlines: %w", err)
	}
	defer rows.Close()

	children := make(map[string][]string)
	for rows.Next() {
		var id string
		var parentID sql.NullString
		if err := rows.Scan(&id, &parentID); err != nil {
			return nil, fmt.Errorf("wiki store: scan outline id: %w", err)
		}
		if parentID.Valid {
			children[parentID.String] = append(children[parentID.String], id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(outlineIDs))
	var out []string
	var walk func(id string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
		for _, c := range children[id] {
			walk(c)
		}
	}
	for _, id := range outlineIDs {
		walk(id)
	}
	return out, nil
}

// UnitIDsByOutlineIDs resolves outline node ids to the current, completed
// knowledge_units filed under them (mirrors retrieval.Store.
// GetUnitsByOutlineIDs, same WHERE clause, unit_id-only projection since the
// caller only needs it to reach knowledge_points next).
func (s *Store) UnitIDsByOutlineIDs(outlineIDs []string) ([]string, error) {
	if len(outlineIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(outlineIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT unit_id FROM knowledge_units
		WHERE outline_id IN (%s) AND status = 'completed' AND lifecycle = 'current'`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: unit ids by outline ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan unit id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PointIDsByUnitIDs returns every lifecycle=current KP in unitIDs —
// deliberately all of them, not retrieval.Store.GetFirstPointByUnitID's
// "one representative KP per unit" (that convention exists for direct-answer
// citation precision; topic-scope candidate search wants breadth).
func (s *Store) PointIDsByUnitIDs(unitIDs []string) ([]string, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(unitIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT point_id FROM knowledge_points
		WHERE unit_id IN (%s) AND lifecycle = 'current'`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: point ids by unit ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan point id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CandidateSemantic is one topic-candidate KP's material for the LLM
// relevance judge (docs/impl/v1/wiki.md 步骤 8 候选检索 1b, 2026-08-07
// 新增) — source_theme/content_theme/intent/object/scope come from
// unit_rerank_semantics, computed once at ingestion time (internal/unit),
// not re-extracted here; a unit that never ran extraction (older data)
// leaves those fields blank rather than excluding the candidate.
type CandidateSemantic struct {
	PointID      string
	Content      string
	UnitCenter   string
	SourceTitle  string
	SourceTheme  string
	ContentTheme string
	Intent       string
	Object       string
	Scope        string
}

// CandidateSemantics batches the join across knowledge_points/
// knowledge_units/unit_rerank_semantics/sources that judgeTopicCandidateRelevance
// needs, in one query rather than one retrieval.Store-style method per field
// (topic-scope candidates are ≤ wiki.topic_candidate_kp_max, so this is
// always a small, single-shot batch — no pagination).
func (s *Store) CandidateSemantics(pointIDs []string) ([]CandidateSemantic, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT kp.point_id, kp.content, ku.center, s.title,
			COALESCE(urs.source_theme, ''), COALESCE(urs.content_theme, ''),
			COALESCE(urs.intent, ''), COALESCE(urs.object, ''), COALESCE(urs.scope, '')
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN sources s ON kp.source_id = s.source_id
		LEFT JOIN unit_rerank_semantics urs ON urs.unit_id = ku.unit_id
		WHERE kp.point_id IN (%s)`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: candidate semantics: %w", err)
	}
	defer rows.Close()

	var out []CandidateSemantic
	for rows.Next() {
		var c CandidateSemantic
		if err := rows.Scan(&c.PointID, &c.Content, &c.UnitCenter, &c.SourceTitle,
			&c.SourceTheme, &c.ContentTheme, &c.Intent, &c.Object, &c.Scope); err != nil {
			return nil, fmt.Errorf("wiki store: scan candidate semantic: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// WizardTask persists 分步向导第 1 步的候选检索进度(docs/impl/v1/wiki.md
// 步骤 8 "分步向导", 2026-08-07 断点续开修订) so it survives a page reload
// or an accidental modal-close — the goroutine that runs
// PreviewTopicCandidates writes status/candidates_json/error_message as it
// progresses, and the frontend resumes from whichever status it finds.
type WizardTask struct {
	TaskID              string
	DomainID            string
	TopicName           string
	TopicDescription    string
	Status              string // candidates_loading | candidates_ready | error
	CandidatesJSON      string // marshaled []TopicCandidateEntry snapshot from the retrieval run
	SelectedMembersJSON string // marshaled []string of human-checked page_ids
	ErrorMessage        string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const (
	WizardTaskStatusCandidatesLoading = "candidates_loading"
	WizardTaskStatusCandidatesReady   = "candidates_ready"
	WizardTaskStatusError             = "error"
)

func (s *Store) InsertWizardTask(t *WizardTask) error {
	_, err := s.db.Exec(`INSERT INTO wiki_wizard_tasks
		(task_id, domain_id, topic_name, topic_description, status, candidates_json, selected_members_json)
		VALUES (?, ?, ?, ?, ?, '[]', '[]')`,
		t.TaskID, t.DomainID, t.TopicName, t.TopicDescription, t.Status)
	if err != nil {
		return fmt.Errorf("wiki store: insert wizard task: %w", err)
	}
	return nil
}

func scanWizardTask(row interface{ Scan(...interface{}) error }) (*WizardTask, error) {
	var t WizardTask
	var errMsg sql.NullString
	if err := row.Scan(&t.TaskID, &t.DomainID, &t.TopicName, &t.TopicDescription, &t.Status,
		&t.CandidatesJSON, &t.SelectedMembersJSON, &errMsg, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.ErrorMessage = errMsg.String
	return &t, nil
}

const wizardTaskSelectCols = `task_id, domain_id, topic_name, topic_description, status,
	candidates_json, selected_members_json, error_message, created_at, updated_at`

// GetWizardTaskByDomain backs "同一领域同时只允许一个进行中的向导任务":
// StartWizardTask checks this before inserting, and resumes the existing row
// instead of starting a second retrieval. Returns (nil, nil) when none.
func (s *Store) GetWizardTaskByDomain(domainID string) (*WizardTask, error) {
	t, err := scanWizardTask(s.db.QueryRow(`SELECT `+wizardTaskSelectCols+`
		FROM wiki_wizard_tasks WHERE domain_id = ?`, domainID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get wizard task by domain: %w", err)
	}
	return t, nil
}

func (s *Store) GetWizardTaskByID(taskID string) (*WizardTask, error) {
	t, err := scanWizardTask(s.db.QueryRow(`SELECT `+wizardTaskSelectCols+`
		FROM wiki_wizard_tasks WHERE task_id = ?`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wiki store: get wizard task by id: %w", err)
	}
	return t, nil
}

// ListActiveWizardTasksByDomain returns every wizard task, keyed for the
// catalog join (docs/impl/v1/wiki.md 步骤 8 "分步向导" 卡片) — at most one
// row per domain_id (UNIQUE), but the caller wants them all in one query
// rather than N GetWizardTaskByDomain round-trips.
func (s *Store) ListWizardTasks() ([]WizardTask, error) {
	rows, err := s.db.Query(`SELECT ` + wizardTaskSelectCols + ` FROM wiki_wizard_tasks`)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list wizard tasks: %w", err)
	}
	defer rows.Close()

	var out []WizardTask
	for rows.Next() {
		t, err := scanWizardTask(rows)
		if err != nil {
			return nil, fmt.Errorf("wiki store: scan wizard task: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateWizardTaskCandidatesReady(taskID, candidatesJSON string) error {
	_, err := s.db.Exec(`UPDATE wiki_wizard_tasks SET status = ?, candidates_json = ?, updated_at = CURRENT_TIMESTAMP
		WHERE task_id = ?`, WizardTaskStatusCandidatesReady, candidatesJSON, taskID)
	if err != nil {
		return fmt.Errorf("wiki store: update wizard task candidates ready: %w", err)
	}
	return nil
}

func (s *Store) UpdateWizardTaskError(taskID, errMsg string) error {
	_, err := s.db.Exec(`UPDATE wiki_wizard_tasks SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE task_id = ?`, WizardTaskStatusError, errMsg, taskID)
	if err != nil {
		return fmt.Errorf("wiki store: update wizard task error: %w", err)
	}
	return nil
}

func (s *Store) UpdateWizardTaskSelectedMembers(taskID, selectedMembersJSON string) error {
	_, err := s.db.Exec(`UPDATE wiki_wizard_tasks SET selected_members_json = ?, updated_at = CURRENT_TIMESTAMP
		WHERE task_id = ?`, selectedMembersJSON, taskID)
	if err != nil {
		return fmt.Errorf("wiki store: update wizard task selected members: %w", err)
	}
	return nil
}

// DeleteWizardTask is idempotent (0 rows affected is not an error) — safe to
// call from a goroutine that finishes after the human already dismissed the
// task, and from the draft-creation success path releasing the domain slot.
func (s *Store) DeleteWizardTask(taskID string) error {
	if _, err := s.db.Exec(`DELETE FROM wiki_wizard_tasks WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("wiki store: delete wizard task: %w", err)
	}
	return nil
}
