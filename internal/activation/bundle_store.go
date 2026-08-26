package activation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const bundleColumns = `bundle_id, cluster_fingerprint, representative_terms, observed_conditions,
	member_point_ids, status, adopt_count, fail_count,
	last_used_at, created_from, status_changed_at, created_at, updated_at`

func encodeBundleMembers(members []BundleMember) (string, error) {
	if members == nil {
		members = []BundleMember{}
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return "", fmt.Errorf("activation store: encode bundle members: %w", err)
	}
	return string(raw), nil
}

func decodeBundleMembers(raw string) ([]BundleMember, error) {
	if raw == "" {
		return nil, nil
	}
	var out []BundleMember
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("activation store: decode bundle members: %w", err)
	}
	return out, nil
}

func scanBundle(row interface{ Scan(...interface{}) error }) (*ActivationBundle, error) {
	var b ActivationBundle
	var observedRaw, memberRaw string
	err := row.Scan(&b.BundleID, &b.ClusterFingerprint, &b.RepresentativeTerms, &observedRaw,
		&memberRaw, &b.Status, &b.AdoptCount, &b.FailCount,
		&b.LastUsedAt, &b.CreatedFrom, &b.StatusChangedAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if b.ObservedConditions, err = decodeObservedConditions(observedRaw); err != nil {
		return nil, err
	}
	if b.Members, err = decodeBundleMembers(memberRaw); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateBundle inserts a new candidate bundle — Study's显影扫描（bundle_scan.go）
// is the only caller; callers must have already Match-ed against every
// non-deprecated existing bundle to avoid a duplicate cluster identity
// (docs/impl/v1/activation-bundle.md 步骤 4「防御性复核」).
func (s *Store) CreateBundle(b *ActivationBundle) error {
	if b.BundleID == "" {
		b.BundleID = uuid.New().String()
	}
	if b.Status == "" {
		b.Status = BundleStatusCandidate
	}
	if b.CreatedFrom == "" {
		b.CreatedFrom = "[]"
	}
	observedJSON, err := encodeObservedConditions(b.ObservedConditions)
	if err != nil {
		return err
	}
	memberJSON, err := encodeBundleMembers(b.Members)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO activation_bundles
		(bundle_id, cluster_fingerprint, representative_terms, observed_conditions,
		 member_point_ids, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.BundleID, b.ClusterFingerprint, b.RepresentativeTerms, observedJSON,
		memberJSON, b.Status, b.CreatedFrom)
	if err != nil {
		return fmt.Errorf("activation store: create bundle: %w", err)
	}
	return nil
}

func (s *Store) GetBundleByID(bundleID string) (*ActivationBundle, error) {
	row := s.db.QueryRow(`SELECT `+bundleColumns+` FROM activation_bundles WHERE bundle_id = ?`, bundleID)
	b, err := scanBundle(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get bundle: %w", err)
	}
	return b, nil
}

// ListBundlesByStatus powers both Match's cache load (all non-deprecated
// statuses) and the read-only management API's status filter.
func (s *Store) ListBundlesByStatus(statuses []string, limit, offset int) ([]ActivationBundle, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + bundleColumns + ` FROM activation_bundles`
	args := []interface{}{}
	if len(statuses) > 0 {
		ph, in := buildPointIDPlaceholders(statuses)
		q += ` WHERE status IN (` + ph + `)`
		args = append(args, in...)
	}
	q += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list bundles: %w", err)
	}
	defer rows.Close()
	var out []ActivationBundle
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan bundle: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListMatchableBundles is the Bundle Matcher's cache source — every bundle
// not yet deprecated (candidate/verified/weakened all still Match, mirroring
// ActivationLink's ListMatchableLinksForCurrentKP intent, but bundles have no
// single owning KP to lifecycle-filter by — member/fringe lifecycle currency
// is enforced separately by Study's per-tick lifecycle sweep, not at Match
// time).
//
// domainIDs scopes the scan to bundles with at least one member point in
// those domains (2026-08-25, BundleMatcher's domain-sharded cache) — checked
// via json_each over the JSON-encoded member_point_ids column (same pattern
// as the trace/study json_each queries elsewhere in this codebase), since
// membership isn't a normalized junction table. Empty domainIDs means
// unresolved domain and loads every bundle system-wide, same as before this
// scoping was added.
func (s *Store) ListMatchableBundles(domainIDs []string) ([]ActivationBundle, error) {
	args := []interface{}{BundleStatusDeprecated}
	query := `SELECT ` + bundleColumns + ` FROM activation_bundles WHERE status != ?`
	if len(domainIDs) > 0 {
		ph := make([]string, len(domainIDs))
		for i, id := range domainIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		query += ` AND EXISTS (
			SELECT 1 FROM json_each(activation_bundles.member_point_ids) je
			JOIN knowledge_points kp ON kp.point_id = json_extract(je.value, '$.point_id')
			JOIN sources src ON src.source_id = kp.source_id
			WHERE src.domain_id IN (` + strings.Join(ph, ",") + `)
		)`
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list matchable bundles: %w", err)
	}
	defer rows.Close()
	var out []ActivationBundle
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan bundle: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// UpdateBundleMembers overwrites member/fringe sets and observed conditions
// in one call — Study's显影扫描 recomputes both together every tick. After
// writing, recomputes and persists the trigger-axis derived status
// (2026-08-13, docs/impl/v1/activation.md「置信度：每条观测条件自己的 Beta
// 后验」applied to Bundle, per the execution plan's "UpdateBundleMembers /
// AppendBundleObservedCondition ... 写后调用 deriveAndPersistBundleStatus").
func (s *Store) UpdateBundleMembers(bundleID string, members []BundleMember, conds []ObservedCondition) error {
	memberJSON, err := encodeBundleMembers(members)
	if err != nil {
		return err
	}
	observedJSON, err := encodeObservedConditions(conds)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE activation_bundles
		SET member_point_ids = ?, observed_conditions = ?, updated_at = CURRENT_TIMESTAMP
		WHERE bundle_id = ?`, memberJSON, observedJSON, bundleID)
	if err != nil {
		return fmt.Errorf("activation store: update bundle members: %w", err)
	}
	return s.deriveAndPersistBundleStatus(bundleID)
}

// RecordMemberOutcome updates a single member's success_count/failure_count
// within a bundle's member axis (docs/impl/v1/activation-bundle.md「成员
// 置信度：Bundle 独有的第二根轴」). Called from trace.recordBundleHitOutcome
// (2026-08-20 阶段 2「验证」接线) once per member point actually used to serve
// a Bundle hit. A pointID not found among the bundle's Members is a no-op
// (not an error): a member may have already been filtered out of a later
// 显影扫描 pass before this outcome was recorded.
func (s *Store) RecordMemberOutcome(bundleID, pointID string, success bool) error {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("activation store: record member outcome: bundle not found: %s", bundleID)
	}
	found := false
	now := time.Now().UTC()
	for i := range b.Members {
		if b.Members[i].PointID != pointID {
			continue
		}
		found = true
		if success {
			b.Members[i].SuccessCount++
		} else {
			b.Members[i].FailureCount++
		}
		b.Members[i].LastSeenAt = now
		break
	}
	if !found {
		return nil
	}
	memberJSON, err := encodeBundleMembers(b.Members)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE activation_bundles SET member_point_ids = ?, updated_at = CURRENT_TIMESTAMP WHERE bundle_id = ?`,
		memberJSON, bundleID)
	if err != nil {
		return fmt.Errorf("activation store: record member outcome: %w", err)
	}
	return nil
}

// RecordBundleOutcome mirrors Store.RecordOutcome for a Bundle's trigger-axis
// condition (docs/impl/v1/activation-bundle.md「验证」, 2026-08-20 阶段 2
// 接线): locates the ObservedCondition matching (subject, intent, audience,
// constraint) exactly — a Bundle only ever carries one such condition in
// practice, since buildBundleObservedConditionsAndMembers always filters
// history down to a single canonical tuple before merging, but the lookup is
// written generically like Link's rather than assuming index 0 — and
// increments its success_count or failure_count. matched=false (no error)
// when no condition matches, mirroring RecordOutcome's contract.
func (s *Store) RecordBundleOutcome(bundleID, subject, intent, audience, constraint string, success bool) (bool, *ActivationBundle, error) {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return false, nil, err
	}
	if b == nil {
		return false, nil, fmt.Errorf("activation store: record bundle outcome: bundle not found: %s", bundleID)
	}
	conds := append([]ObservedCondition(nil), b.ObservedConditions...)
	idx := findConditionIndex(conds, subject, intent, audience, constraint)
	if idx < 0 {
		return false, b, nil
	}
	if success {
		conds[idx].SuccessCount++
	} else {
		conds[idx].FailureCount++
	}
	conds[idx].LastSeenAt = time.Now().UTC()
	if err := s.UpdateBundleMembers(bundleID, b.Members, conds); err != nil {
		return false, nil, err
	}
	adoptDelta, failDelta := 0, 0
	if success {
		adoptDelta = 1
	} else {
		failDelta = 1
	}
	if err := s.UpdateBundleStats(bundleID, adoptDelta, failDelta); err != nil {
		return false, nil, err
	}
	updated, err := s.GetBundleByID(bundleID)
	if err != nil {
		return false, nil, err
	}
	return true, updated, nil
}

// UpdateBundleStats mirrors Store.UpdateStats for ActivationLink — cosmetic
// display counters (bundleResp.AdoptCount/FailCount), not consulted by Match
// or deriveAndPersistBundleStatus, which read ObservedConditions directly.
func (s *Store) UpdateBundleStats(bundleID string, adoptDelta, failDelta int) error {
	_, err := s.db.Exec(`UPDATE activation_bundles
		SET adopt_count = adopt_count + ?, fail_count = fail_count + ?, updated_at = CURRENT_TIMESTAMP
		WHERE bundle_id = ?`, adoptDelta, failDelta, bundleID)
	if err != nil {
		return fmt.Errorf("activation store: update bundle stats: %w", err)
	}
	return nil
}

// RefreshBundleMembers merges Study's periodically recomputed candidate
// members/conditions into a bundle's existing state without discarding any
// live-accumulated RecordBundleOutcome/RecordMemberOutcome counts
// (docs/impl/v1/activation-bundle.md「验证」, 2026-08-20 改判 — replaces the
// prior direct UpdateBundleMembers(matched.BundleID, members, conds) call in
// study/bundle_scan.go, which unconditionally overwrote both axes every
// Study tick and so could never let live serving feedback accumulate). A
// candidate member/condition that already exists keeps its stored
// success_count/failure_count — only LastSeenAt (and, for conditions,
// KnownQuestionTerms) refresh; a brand-new candidate is inserted with the
// recomputed seed values from this rebuild; an existing member the rebuild no
// longer surfaces is left in place — removal is lifecycle-driven
// (weakenBundlesWithExpiredCoreMembers), not a side effect of a quiet rebuild.
// UpdateBundleMembers itself is untouched and keeps doing a raw overwrite —
// AppendBundleObservedCondition already computes the exact final state it
// wants written and calling through another merge pass would double-count.
func (s *Store) RefreshBundleMembers(bundleID string, candidateMembers []BundleMember, candidateConds []ObservedCondition) error {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("activation store: refresh bundle members: not found: %s", bundleID)
	}

	members := append([]BundleMember(nil), b.Members...)
	existingByPoint := make(map[string]int, len(members))
	for i, m := range members {
		existingByPoint[m.PointID] = i
	}
	for _, cand := range candidateMembers {
		if idx, ok := existingByPoint[cand.PointID]; ok {
			members[idx].LastSeenAt = cand.LastSeenAt
			continue
		}
		members = append(members, cand)
		existingByPoint[cand.PointID] = len(members) - 1
	}

	conds := append([]ObservedCondition(nil), b.ObservedConditions...)
	existingByKey := make(map[string]int, len(conds))
	for i, c := range conds {
		existingByKey[conditionKey(c)] = i
	}
	for _, cand := range candidateConds {
		k := conditionKey(cand)
		if idx, ok := existingByKey[k]; ok {
			conds[idx].LastSeenAt = cand.LastSeenAt
			conds[idx].KnownQuestionTerms = mergeKnownQuestionTerms(conds[idx].KnownQuestionTerms, cand.QuestionTerms)
			continue
		}
		conds = append(conds, cand)
		existingByKey[k] = len(conds) - 1
	}

	return s.UpdateBundleMembers(bundleID, members, conds)
}

// AppendBundleObservedCondition merges one quadruple into an existing bundle
// — mirrors Store.AppendObservedCondition for ActivationLink, adapted to
// preserve MemberPointIDs/FringePointIDs untouched (Retrieval's real-time
// cross-unit-ambiguity formation path, docs/impl/v1/activation-bundle.md
// 步骤 4b).
func (s *Store) AppendBundleObservedCondition(bundleID string, add ObservedCondition, max int) error {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("activation store: append bundle observed condition: not found: %s", bundleID)
	}
	merged := MergeObservedConditions(b.ObservedConditions, add, max)
	return s.UpdateBundleMembers(bundleID, b.Members, merged)
}

// deriveAndPersistBundleStatus recomputes the trigger-axis derived status
// (candidate/verified — deprecated for Bundle is only ever set directly by
// lifecycle-driven logic, see study/bundle_scan.go
// weakenBundlesWithExpiredCoreMembers, not derived here) and writes it if it
// changed. Bundle's confidenceCfg is shared with Matcher/BundleMatcher via
// Service.SetConfidenceConfig → Store.SetConfidenceConfig.
func (s *Store) deriveAndPersistBundleStatus(bundleID string) error {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if b.Status == BundleStatusDeprecated {
		return nil
	}
	newStatus := deriveStatus(b.ObservedConditions, s.confidenceCfg)
	if newStatus == b.Status {
		return nil
	}
	return s.UpdateBundleStatus(bundleID, newStatus)
}

// ResetBundle implements the bundle-detail「清空重来」action (mirrors
// Store's role in Service.Reject for ActivationLink, docs/impl/v1/
// activation-bundle.md「成员置信度」): wipes BOTH confidence axes — the
// trigger-axis ObservedConditions AND every member's SuccessCount/
// FailureCount — back to zero, leaving the member point_id list itself
// intact so accumulation can start over from the same membership rather
// than losing it. Status is re-derived afterward (lands on candidate for
// empty conditions, same as Link).
func (s *Store) ResetBundle(bundleID string) error {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("activation store: reset bundle: bundle not found: %s", bundleID)
	}
	members := append([]BundleMember(nil), b.Members...)
	for i := range members {
		members[i].SuccessCount = 0
		members[i].FailureCount = 0
	}
	return s.UpdateBundleMembers(bundleID, members, []ObservedCondition{})
}

// UpdateBundleStatus is a plain status setter (mirrors Store.UpdateStatus for
// ActivationLink) — used both by deriveAndPersistBundleStatus and by
// study/bundle_scan.go's lifecycle-driven direct deprecation.
func (s *Store) UpdateBundleStatus(bundleID, status string) error {
	_, err := s.db.Exec(`UPDATE activation_bundles
		SET status = ?, status_changed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE bundle_id = ?`, status, bundleID)
	if err != nil {
		return fmt.Errorf("activation store: update bundle status: %w", err)
	}
	return nil
}

func (s *Store) TouchBundleLastUsed(bundleID string) error {
	_, err := s.db.Exec(`UPDATE activation_bundles SET last_used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE bundle_id = ?`, bundleID)
	if err != nil {
		return fmt.Errorf("activation store: touch bundle last used: %w", err)
	}
	return nil
}

// ListBundleMatchedQuestions returns traces whose activation_bundle_ids
// contain bundleID — mirrors Store.ListMatchedQuestions for ActivationLink
// (docs/impl/v1/activation-bundle.md「命中问法」).
func (s *Store) ListBundleMatchedQuestions(bundleID string) ([]LinkQuestion, error) {
	rows, err := s.db.Query(`
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t, json_each(t.activation_bundle_ids) AS j
		WHERE j.value = ?
		ORDER BY t.created_at ASC`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("activation store: list bundle matched questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

// ListBundleCreatedFromQuestions resolves created_from — a JSON array of
// trace_ids (bundle's own convention: it显影 directly from a window of
// traces, not from learning_events like ActivationLink does) — back into
// their originating questions.
func (s *Store) ListBundleCreatedFromQuestions(bundleID string) ([]LinkQuestion, error) {
	b, err := s.GetBundleByID(bundleID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	var traceIDs []string
	if err := json.Unmarshal([]byte(b.CreatedFrom), &traceIDs); err != nil || len(traceIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPointIDPlaceholders(traceIDs)
	rows, err := s.db.Query(`SELECT trace_id, question, created_at, path_type, retrieval_quality
		FROM traces WHERE trace_id IN (`+ph+`) ORDER BY created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list bundle questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}
