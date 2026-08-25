package activation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB

	mu            sync.RWMutex
	confidenceCfg ConfidenceConfig
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetConfidenceConfig wires the shared retrieval.* confidence knobs
// (docs/impl/v1/activation.md 配置项) — used by deriveAndPersistBundleStatus.
// Service.SetConfidenceConfig calls this alongside Matcher/BundleMatcher's
// own setters so all three read the same values.
func (s *Store) SetConfidenceConfig(cfg ConfidenceConfig) {
	s.mu.Lock()
	s.confidenceCfg = cfg
	s.mu.Unlock()
}

func (s *Store) getConfidenceConfig() ConfidenceConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.confidenceCfg
}

const linkColumns = `link_id, question_terms, subject_terms, intent_terms, audience,
	constraint_terms, observed_conditions, scene, goal, point_id, status, adopt_count, fail_count,
	last_used_at, created_from, status_changed_at, created_at, updated_at`

// encodeTermSet sorts and JSON-encodes an accumulated condition set
// (intent_terms/audience/constraint_terms) — sorting makes the stored form
// deterministic regardless of the caller's map/slice iteration order, which
// both change-detection (conditionEqual in study) and byte-for-byte test
// assertions rely on. nil encodes as "[]", never "null".
func encodeTermSet(values []string) (string, error) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	if sorted == nil {
		sorted = []string{}
	}
	b, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("activation store: encode term set: %w", err)
	}
	return string(b), nil
}

// decodeTermSet parses a stored condition-set column. "" is treated as an
// empty set (defensive — migration 027 rewrites every row to valid JSON, but
// a blank column should degrade to "no values" rather than fail the scan).
func decodeTermSet(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("activation store: decode term set %q: %w", raw, err)
	}
	return values, nil
}

func scanLink(row interface{ Scan(...interface{}) error }) (*ActivationLink, error) {
	var l ActivationLink
	var intentRaw, audienceRaw, constraintRaw, observedRaw string
	err := row.Scan(&l.LinkID, &l.QuestionTerms, &l.SubjectTerms, &intentRaw, &audienceRaw,
		&constraintRaw, &observedRaw, &l.Scene, &l.Goal, &l.PointID, &l.Status, &l.AdoptCount, &l.FailCount,
		&l.LastUsedAt, &l.CreatedFrom, &l.StatusChangedAt, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if l.IntentTerms, err = decodeTermSet(intentRaw); err != nil {
		return nil, err
	}
	if l.Audience, err = decodeTermSet(audienceRaw); err != nil {
		return nil, err
	}
	if l.ConstraintTerms, err = decodeTermSet(constraintRaw); err != nil {
		return nil, err
	}
	if l.ObservedConditions, err = decodeObservedConditions(observedRaw); err != nil {
		return nil, err
	}
	return &l, nil
}

// InsertLink inserts a new candidate link. Callers must have already checked
// for an existing (question_terms, point_id) row — see Service.CreateLink for
// the idempotency/deprecated-reject logic that belongs in front of this.
func (s *Store) InsertLink(l *ActivationLink) error {
	if l.LinkID == "" {
		l.LinkID = uuid.New().String()
	}
	if l.Status == "" {
		l.Status = StatusCandidate
	}
	if l.CreatedFrom == "" {
		l.CreatedFrom = "[]"
	}
	applyLegacyProjection(l)
	intentJSON, err := encodeTermSet(l.IntentTerms)
	if err != nil {
		return err
	}
	audienceJSON, err := encodeTermSet(l.Audience)
	if err != nil {
		return err
	}
	constraintJSON, err := encodeTermSet(l.ConstraintTerms)
	if err != nil {
		return err
	}
	observedJSON, err := encodeObservedConditions(l.ObservedConditions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO activation_links
		(link_id, question_terms, subject_terms, intent_terms, audience, constraint_terms,
		 observed_conditions, point_id, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.LinkID, l.QuestionTerms, l.SubjectTerms, intentJSON, audienceJSON, constraintJSON,
		observedJSON, l.PointID, l.Status, l.CreatedFrom)
	if err != nil {
		return fmt.Errorf("activation store: insert link: %w", err)
	}
	return nil
}

func applyLegacyProjection(l *ActivationLink) {
	if len(l.ObservedConditions) == 0 {
		return
	}
	subj, intent, aud, cons := ProjectLegacyFields(l.ObservedConditions)
	l.SubjectTerms = subj
	l.IntentTerms = intent
	l.Audience = aud
	l.ConstraintTerms = cons
}

// PointUnitInfo resolves a link's target KP into displayable context for the
// detail dialog: the KP content, its owning KU's id/center, and the source
// document title (the product/document a link belongs to lives nowhere else
// in the dialog — same-label links are otherwise indistinguishable). Missing
// point (shouldn't happen — FK) degrades to empty strings.
func (s *Store) PointUnitInfo(pointID string) (pointContent, unitID, unitCenter, sourceTitle string, err error) {
	err = s.db.QueryRow(`
		SELECT kp.content, ku.unit_id, ku.center, s.title
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN sources s ON s.source_id = kp.source_id
		WHERE kp.point_id = ?`, pointID).Scan(&pointContent, &unitID, &unitCenter, &sourceTitle)
	if err == sql.ErrNoRows {
		return "", "", "", "", nil
	}
	if err != nil {
		return "", "", "", "", fmt.Errorf("activation store: point unit info: %w", err)
	}
	return pointContent, unitID, unitCenter, sourceTitle, nil
}

// PointLifecycleCurrent reports whether pointID's KP is still lifecycle=current
// (docs/impl/v1/activation.md「与旧状态机的映射」deprecated 判定的输入; a
// package-local query mirroring study.Store.PointLifecycleCurrent — kept
// per-package rather than shared, same precedent as other small lookups in
// this codebase).
func (s *Store) PointLifecycleCurrent(pointID string) (bool, error) {
	var lifecycle string
	err := s.db.QueryRow(`SELECT lifecycle FROM knowledge_points WHERE point_id = ?`, pointID).Scan(&lifecycle)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("activation store: point lifecycle: %w", err)
	}
	return lifecycle == "current", nil
}

func (s *Store) GetByID(linkID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE link_id = ?`, linkID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by id: %w", err)
	}
	return l, nil
}

// GetByPointID returns the point's single link (idx_al_point_id is a UNIQUE
// index — at most one row can exist), or nil if none. This is the identity
// check Service.CreateLink uses now that point_id, not (question_terms,
// point_id), is the dedup key (docs/impl/v1/activation.md 数据结构).
func (s *Store) GetByPointID(pointID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE point_id = ?`, pointID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by point id: %w", err)
	}
	return l, nil
}

func (s *Store) GetByQuestionAndPoint(questionTerms, pointID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE question_terms = ? AND point_id = ?`,
		questionTerms, pointID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by question/point: %w", err)
	}
	return l, nil
}

// UpdateStatus is the only place status_changed_at / updated_at are written
// for a transition. Callers must have already validated the transition is
// legal (see Service.TransitionLink).
func (s *Store) UpdateStatus(linkID, status string) error {
	_, err := s.db.Exec(`UPDATE activation_links
		SET status = ?, status_changed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, status, linkID)
	if err != nil {
		return fmt.Errorf("activation store: update status: %w", err)
	}
	return nil
}

// UpdateConditions / ReplaceObservedConditions overwrite a link's observed
// condition groups in place (Study full rebuild). Legacy columns are projected
// from the newest group for old UI.
func (s *Store) UpdateConditions(linkID string, cond LinkCondition) error {
	return s.ReplaceObservedConditions(linkID, cond.EffectiveConditions())
}

// maxKnownQuestionTerms caps each ObservedCondition.KnownQuestionTerms — a
// set of literal question term-strings, unbounded growth risk is real for a
// heavily-reused condition, so cap generously and trim deterministically
// (alphabetical, since the set carries no per-entry recency) rather than
// let it grow forever. (2026-08-13: moved down from a link-level column to
// per-condition, see conditions.go mergeKnownQuestionTerms — this constant
// is the single source of truth both places reference.)
const maxKnownQuestionTerms = 200

// ReplaceObservedConditions writes the full observed_conditions list,
// projects legacy fields, and (2026-08-13) recomputes/persists the derived
// status from the new condition set (docs/impl/v1/activation.md「置信度计算
// 与缓存」: every write path that changes observed_conditions keeps status
// fresh, not just RecordOutcome/RecordAuditOutcome, so Wiki's
// status='verified' reads never go stale). known_question_terms now lives
// per-condition (conditions.go), no table-level read-merge-write needed.
func (s *Store) ReplaceObservedConditions(linkID string, conds []ObservedCondition) error {
	if conds == nil {
		conds = []ObservedCondition{}
	}
	subj, intent, aud, cons := ProjectLegacyFields(conds)
	intentJSON, err := encodeTermSet(intent)
	if err != nil {
		return err
	}
	audienceJSON, err := encodeTermSet(aud)
	if err != nil {
		return err
	}
	constraintJSON, err := encodeTermSet(cons)
	if err != nil {
		return err
	}
	observedJSON, err := encodeObservedConditions(conds)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE activation_links
		SET subject_terms = ?, intent_terms = ?, audience = ?, constraint_terms = ?,
		    observed_conditions = ?, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, subj, intentJSON, audienceJSON, constraintJSON, observedJSON, linkID)
	if err != nil {
		return fmt.Errorf("activation store: replace observed conditions: %w", err)
	}
	return s.deriveAndPersistStatus(linkID, conds)
}

// deriveAndPersistStatus recomputes the candidate/verified derived status
// from conds and writes it if changed — deprecated is never derived here (it
// depends on KP lifecycle, an external fact this Store-level helper has no
// business overriding; only the explicit UpdateStatus(..., StatusDeprecated)
// callers, driven by lifecycle notification, set that value). Store-level
// counterpart to Service.deriveAndPersistStatus, used by every
// observed_conditions write path (docs/impl/v1/activation.md「置信度计算与
// 缓存」).
func (s *Store) deriveAndPersistStatus(linkID string, conds []ObservedCondition) error {
	var currentStatus string
	if err := s.db.QueryRow(`SELECT status FROM activation_links WHERE link_id = ?`, linkID).Scan(&currentStatus); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("activation store: derive status: read current: %w", err)
	}
	if currentStatus == StatusDeprecated {
		return nil
	}
	newStatus := deriveStatus(conds, s.getConfidenceConfig())
	if newStatus == currentStatus {
		return nil
	}
	return s.UpdateStatus(linkID, newStatus)
}

// AppendObservedCondition merges one quadruple into the link (slow-path enrichment).
func (s *Store) AppendObservedCondition(linkID string, add ObservedCondition, max int) error {
	link, err := s.GetByID(linkID)
	if err != nil {
		return err
	}
	if link == nil {
		return fmt.Errorf("activation store: append: link not found: %s", linkID)
	}
	merged := MergeObservedConditions(link.ObservedConditions, add, max)
	return s.ReplaceObservedConditions(linkID, merged)
}

// RecordOutcome locates the observed condition matching (subject, intent,
// audience, constraint) exactly and increments its success_count or
// failure_count (docs/impl/v1/activation.md 步骤 1). questionTerms, when
// non-empty, folds into that condition's known_question_terms (the literal-
// question-shortcut registration entry point — success or failure both
// register, since the shortcut answers "route to which condition", not
// "is this condition trustworthy"). Writes through ReplaceObservedConditions
// so legacy projection and derived status stay consistent with every other
// write path. matched=false (no error) when no condition matches — this is
// a Store-level primitive; RecordOutcome should only be called right after
// Match() returned a hit against this exact condition, so a miss here is
// unexpected but not fatal to the caller's turn.
func (s *Store) RecordOutcome(linkID, subject, intent, audience, constraint string, success bool, questionTerms string) (bool, *ActivationLink, error) {
	link, err := s.GetByID(linkID)
	if err != nil {
		return false, nil, err
	}
	if link == nil {
		return false, nil, fmt.Errorf("activation store: record outcome: link not found: %s", linkID)
	}
	conds := append([]ObservedCondition(nil), link.ObservedConditions...)
	idx := findConditionIndex(conds, subject, intent, audience, constraint)
	if idx < 0 {
		return false, link, nil
	}
	if success {
		conds[idx].SuccessCount++
	} else {
		conds[idx].FailureCount++
	}
	conds[idx].LastSeenAt = time.Now().UTC()
	if questionTerms != "" {
		conds[idx].KnownQuestionTerms = mergeKnownQuestionTerms(conds[idx].KnownQuestionTerms, questionTerms)
	}
	if err := s.ReplaceObservedConditions(linkID, conds); err != nil {
		return false, nil, err
	}
	adoptDelta, failDelta := 0, 0
	if success {
		adoptDelta = 1
	} else {
		failDelta = 1
	}
	if err := s.UpdateStats(linkID, adoptDelta, failDelta); err != nil {
		return false, nil, err
	}
	updated, err := s.GetByID(linkID)
	if err != nil {
		return false, nil, err
	}
	return true, updated, nil
}

// RecordAuditOutcome locates the same way as RecordOutcome and increments
// success_count/failure_count AND audited_success_count/audited_failure_count
// together in the same call — the two pairs are never updated independently,
// which is what keeps audited_* <= success/failure invariant always true
// (docs/impl/v1/activation.md 步骤 1). agree=true means the independent slow-
// path verification agreed with the fast-path serving; agree=false means it
// didn't, and the slow-path result is treated as ground truth (a failure).
func (s *Store) RecordAuditOutcome(linkID, subject, intent, audience, constraint string, agree bool) (bool, *ActivationLink, error) {
	link, err := s.GetByID(linkID)
	if err != nil {
		return false, nil, err
	}
	if link == nil {
		return false, nil, fmt.Errorf("activation store: record audit outcome: link not found: %s", linkID)
	}
	conds := append([]ObservedCondition(nil), link.ObservedConditions...)
	idx := findConditionIndex(conds, subject, intent, audience, constraint)
	if idx < 0 {
		return false, link, nil
	}
	if agree {
		conds[idx].SuccessCount++
		conds[idx].AuditedSuccessCount++
	} else {
		conds[idx].FailureCount++
		conds[idx].AuditedFailureCount++
	}
	conds[idx].LastSeenAt = time.Now().UTC()
	if err := s.ReplaceObservedConditions(linkID, conds); err != nil {
		return false, nil, err
	}
	adoptDelta, failDelta := 0, 0
	if agree {
		adoptDelta = 1
	} else {
		failDelta = 1
	}
	if err := s.UpdateStats(linkID, adoptDelta, failDelta); err != nil {
		return false, nil, err
	}
	updated, err := s.GetByID(linkID)
	if err != nil {
		return false, nil, err
	}
	return true, updated, nil
}

// findConditionIndex is the exact-four-tuple lookup RecordOutcome/
// RecordAuditOutcome share with MatchConditionGroups' per-condition
// comparison semantics (docs/impl/v1/activation.md 步骤 1: "同 Match
// ConditionGroups 的逐条件比较逻辑，复用，不重新实现" in spirit — the
// existing MatchConditionGroups returns a bool over the whole list, not an
// index, so this is the index-returning sibling rather than a literal call
// site reuse).
func findConditionIndex(conds []ObservedCondition, subject, intent, audience, constraint string) int {
	for i, c := range conds {
		if c.Subject == subject && c.Intent == intent && c.Audience == audience && c.Constraint == constraint {
			return i
		}
	}
	return -1
}

func (s *Store) UpdateStats(linkID string, adoptDelta, failDelta int) error {
	_, err := s.db.Exec(`UPDATE activation_links
		SET adopt_count = adopt_count + ?, fail_count = fail_count + ?, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, adoptDelta, failDelta, linkID)
	if err != nil {
		return fmt.Errorf("activation store: update stats: %w", err)
	}
	return nil
}

func (s *Store) TouchLastUsed(linkIDs []string) error {
	if len(linkIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(linkIDs))
	for i, id := range linkIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	_, err := s.db.Exec(fmt.Sprintf(
		`UPDATE activation_links SET last_used_at = CURRENT_TIMESTAMP WHERE link_id IN (%s)`, placeholders),
		args...)
	if err != nil {
		return fmt.Errorf("activation store: touch last used: %w", err)
	}
	return nil
}

// ListMatchableLinksForCurrentKP loads every link whose target KP is
// lifecycle=current — no status filter (2026-08-13, docs/impl/v1/
// activation.md「候选加载不再按 status 过滤」): status is itself a value
// derived from the same per-condition data Match() is about to score, so
// filtering candidates out by status here would be filtering by a
// conclusion this call hasn't computed yet. deprecated links are excluded
// implicitly — deprecated is defined purely by "target KP lifecycle !=
// current" (see confidence.go deriveStatus), and this query's lifecycle
// JOIN already achieves exactly that filter.
//
// domainIDs scopes the scan to sources in those domains via a JOIN on
// sources.domain_id (2026-08-25, Matcher's domain-sharded cache) — empty
// domainIDs means unresolved domain and loads every link system-wide, same
// as before this scoping was added.
func (s *Store) ListMatchableLinksForCurrentKP(domainIDs []string) ([]ActivationLink, error) {
	return s.listLinksForCurrentKP(domainIDs, StatusVerified, StatusCandidate)
}

// ListVerifiedConditionGroups loads observed_conditions from verified links
// whose KP source is in domainIDs (newest last_used first), capped at limit.
// Used for session_normalize_tuple vocab and for accepting normalized tuples.
func (s *Store) ListVerifiedConditionGroups(domainIDs []string, limit int) ([]ObservedCondition, error) {
	if len(domainIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	ph := make([]string, len(domainIDs))
	args := make([]interface{}, 0, len(domainIDs)+1)
	for i, id := range domainIDs {
		ph[i] = "?"
		args = append(args, id)
	}
	args = append(args, StatusVerified, "current")

	rows, err := s.db.Query(`
		SELECT al.observed_conditions, al.last_used_at
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id
		JOIN sources src ON src.source_id = kp.source_id
		WHERE src.domain_id IN (`+strings.Join(ph, ",")+`)
		  AND al.status = ? AND kp.lifecycle = ?
		  AND src.shadow_of IS NULL
		ORDER BY al.last_used_at IS NULL, al.last_used_at DESC, al.updated_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list condition groups: %w", err)
	}
	defer rows.Close()

	out := make([]ObservedCondition, 0, limit)
	for rows.Next() {
		var raw string
		var lastUsed interface{}
		if err := rows.Scan(&raw, &lastUsed); err != nil {
			return nil, fmt.Errorf("activation store: scan condition groups: %w", err)
		}
		conds, err := decodeObservedConditions(raw)
		if err != nil {
			continue
		}
		for _, c := range conds {
			if len(out) >= limit {
				break
			}
			out = append(out, c)
		}
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FormatVerifiedConditionGroups renders verified links' observed_conditions
// whose KP source is in domainIDs, for session_normalize_tuple.md. Returns
// the prompt text and the number of condition groups included.
func (s *Store) FormatVerifiedConditionGroups(domainIDs []string, limit int) (string, int, error) {
	conds, err := s.ListVerifiedConditionGroups(domainIDs, limit)
	if err != nil {
		return "", 0, err
	}
	if len(conds) == 0 {
		return "（无）", 0, nil
	}
	var b strings.Builder
	for _, c := range conds {
		fmt.Fprintf(&b, "- subject=%s | intent=%s | audience=%s | constraint=%s\n",
			c.Subject, c.Intent, c.Audience, c.Constraint)
	}
	return b.String(), len(conds), nil
}

func (s *Store) listLinksForCurrentKP(domainIDs []string, statuses ...string) ([]ActivationLink, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(statuses)+len(domainIDs)+1)
	for i, st := range statuses {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, st)
	}
	args = append(args, "current")

	query := `SELECT ` + linkColumnsPrefixed("al") + `
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id`
	if len(domainIDs) > 0 {
		query += `
		JOIN sources src ON src.source_id = kp.source_id`
	}
	query += `
		WHERE al.status IN (` + placeholders + `) AND kp.lifecycle = ?`
	if len(domainIDs) > 0 {
		ph := make([]string, len(domainIDs))
		for i, id := range domainIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		query += ` AND src.domain_id IN (` + strings.Join(ph, ",") + `)`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list matchable links: %w", err)
	}
	defer rows.Close()

	var links []ActivationLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan matchable link: %w", err)
		}
		links = append(links, *l)
	}
	return links, rows.Err()
}

func linkColumnsPrefixed(alias string) string {
	cols := []string{"link_id", "question_terms", "subject_terms", "intent_terms", "audience",
		"constraint_terms", "observed_conditions", "scene", "goal", "point_id", "status", "adopt_count", "fail_count",
		"last_used_at", "created_from", "status_changed_at", "created_at", "updated_at"}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

// ListLinksFilter mirrors GET /activation-links query params
// (docs/impl/v1/activation.md 步骤 3).
type ListLinksFilter struct {
	Status  string
	PointID string
	// PointIDs, when non-empty, fetches every link for a known bounded set
	// of points in one call — the 知识地图 concept-page modal's per-KP link
	// badges need this (up to hundreds of KPs per concept), where N calls
	// with PointID would be prohibitive. Mutually exclusive with PointID in
	// practice (both may be set, both apply — AND — but callers don't do
	// that). When set and Limit is 0, defaults to a bulk-sized limit instead
	// of ListLinks' normal browse-page default (see below).
	PointIDs []string
	Limit    int
	Offset   int
}

func buildPointIDPlaceholders(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *Store) ListLinks(f ListLinksFilter) ([]ActivationLinkListRow, error) {
	limit := f.Limit
	if limit <= 0 {
		if len(f.PointIDs) > 0 {
			limit = 5000
		} else {
			limit = 50
		}
	}

	query := `SELECT ` + linkColumnsPrefixed("al") + `, kp.content AS point_summary, ku.center AS unit_center
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE 1 = 1`
	var args []interface{}
	if f.Status != "" {
		query += ` AND al.status = ?`
		args = append(args, f.Status)
	}
	if f.PointID != "" {
		query += ` AND al.point_id = ?`
		args = append(args, f.PointID)
	}
	if len(f.PointIDs) > 0 {
		ph, phArgs := buildPointIDPlaceholders(f.PointIDs)
		query += ` AND al.point_id IN (` + ph + `)`
		args = append(args, phArgs...)
	}
	query += ` ORDER BY al.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list links: %w", err)
	}
	defer rows.Close()

	var results []ActivationLinkListRow
	for rows.Next() {
		var r ActivationLinkListRow
		var intentRaw, audienceRaw, constraintRaw, observedRaw string
		err := rows.Scan(&r.LinkID, &r.QuestionTerms, &r.SubjectTerms, &intentRaw, &audienceRaw,
			&constraintRaw, &observedRaw, &r.Scene, &r.Goal, &r.PointID, &r.Status, &r.AdoptCount, &r.FailCount,
			&r.LastUsedAt, &r.CreatedFrom, &r.StatusChangedAt, &r.CreatedAt, &r.UpdatedAt,
			&r.PointSummary, &r.UnitCenter)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan list row: %w", err)
		}
		if r.IntentTerms, err = decodeTermSet(intentRaw); err != nil {
			return nil, err
		}
		if r.Audience, err = decodeTermSet(audienceRaw); err != nil {
			return nil, err
		}
		if r.ConstraintTerms, err = decodeTermSet(constraintRaw); err != nil {
			return nil, err
		}
		if r.ObservedConditions, err = decodeObservedConditions(observedRaw); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) InsertLearningResult(lr *LearningResult) error {
	if lr.ResultID == "" {
		lr.ResultID = uuid.New().String()
	}
	if lr.Status == "" {
		lr.Status = ResultApplied
	}
	if lr.EventIDs == "" {
		lr.EventIDs = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status, confirmed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		lr.ResultID, lr.Action, lr.ObjectType, lr.ObjectID, lr.Reason, lr.EventIDs, lr.Status, lr.ConfirmedBy)
	if err != nil {
		return fmt.Errorf("activation store: insert learning result: %w", err)
	}
	return nil
}

// ResolvePending is a generic learning_results status updater — still used
// by Wiki's own pending_confirm rows (wiki_candidate, topic_page_candidate),
// unrelated to the removed ActivationLink promote/weaken transition flow
// (docs/impl/v1/wiki.md 步骤 2/8).
func (s *Store) ResolvePending(resultID, status, confirmedBy string) error {
	_, err := s.db.Exec(`UPDATE learning_results
		SET status = ?, confirmed_by = ?, updated_at = CURRENT_TIMESTAMP
		WHERE result_id = ?`, status, confirmedBy, resultID)
	if err != nil {
		return fmt.Errorf("activation store: resolve pending: %w", err)
	}
	return nil
}

func (s *Store) ListLearningResultsByObject(objectType, objectID string) ([]LearningResult, error) {
	rows, err := s.db.Query(`SELECT result_id, action, object_type, object_id, reason, event_ids,
		status, confirmed_by, created_at, updated_at
		FROM learning_results WHERE object_type = ? AND object_id = ? ORDER BY created_at ASC`,
		objectType, objectID)
	if err != nil {
		return nil, fmt.Errorf("activation store: list learning results: %w", err)
	}
	defer rows.Close()

	var results []LearningResult
	for rows.Next() {
		var lr LearningResult
		if err := rows.Scan(&lr.ResultID, &lr.Action, &lr.ObjectType, &lr.ObjectID, &lr.Reason,
			&lr.EventIDs, &lr.Status, &lr.ConfirmedBy, &lr.CreatedAt, &lr.UpdatedAt); err != nil {
			return nil, fmt.Errorf("activation store: scan learning result: %w", err)
		}
		results = append(results, lr)
	}
	return results, rows.Err()
}

// LinkQuestion is one original question associated with an ActivationLink
// (docs/superpowers/specs/2026-07-22-activation-link-questions-ui-design.md).
type LinkQuestion struct {
	Question         string `json:"question"`
	TraceID          string `json:"trace_id"`
	CreatedAt        string `json:"created_at"`
	PathType         string `json:"path_type,omitempty"`
	RetrievalQuality string `json:"retrieval_quality,omitempty"`
}

// ListMatchedQuestions returns traces whose activation_link_ids contain linkID
// (candidate signal hits and verified path hits).
func (s *Store) ListMatchedQuestions(linkID string) ([]LinkQuestion, error) {
	rows, err := s.db.Query(`
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t, json_each(t.activation_link_ids) AS j
		WHERE j.value = ?
		ORDER BY t.created_at ASC`, linkID)
	if err != nil {
		return nil, fmt.Errorf("activation store: list matched questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

// ListCreatedFromQuestions returns create-time fuel questions for
// learning_event IDs stored in activation_links.created_from.
func (s *Store) ListCreatedFromQuestions(eventIDs []string) ([]LinkQuestion, error) {
	if len(eventIDs) == 0 {
		return []LinkQuestion{}, nil
	}
	placeholders := make([]string, len(eventIDs))
	args := make([]interface{}, len(eventIDs))
	for i, id := range eventIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t
		WHERE t.trace_id IN (
			SELECT DISTINCT le.trace_id FROM learning_events le
			WHERE le.event_id IN (` + strings.Join(placeholders, ",") + `)
		)
		ORDER BY t.created_at ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list created_from questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

// ListConfidentQuestionsForPointBefore returns confident traces that cited
// pointID and occurred at or before before (link creation time). Covers
// legacy cooccurrence creates whose created_from held link_candidate IDs
// instead of learning_event IDs.
func (s *Store) ListConfidentQuestionsForPointBefore(pointID string, before time.Time) ([]LinkQuestion, error) {
	rows, err := s.db.Query(`
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t, json_each(t.direct_point_ids) AS pid
		WHERE t.retrieval_quality = 'confident'
		  AND pid.value = ?
		  AND datetime(t.created_at) <= datetime(?)
		ORDER BY t.created_at ASC`, pointID, before)
	if err != nil {
		return nil, fmt.Errorf("activation store: list confident questions for point: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

func scanLinkQuestions(rows *sql.Rows) ([]LinkQuestion, error) {
	out := make([]LinkQuestion, 0)
	for rows.Next() {
		var q LinkQuestion
		var createdAt time.Time
		if err := rows.Scan(&q.TraceID, &q.Question, &createdAt, &q.PathType, &q.RetrievalQuality); err != nil {
			return nil, fmt.Errorf("activation store: scan link question: %w", err)
		}
		q.CreatedAt = createdAt.Format("2006-01-02T15:04:05Z07:00")
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
