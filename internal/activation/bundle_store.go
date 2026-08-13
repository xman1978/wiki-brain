package activation

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
func (s *Store) ListMatchableBundles() ([]ActivationBundle, error) {
	rows, err := s.db.Query(`SELECT `+bundleColumns+` FROM activation_bundles WHERE status != ?`, BundleStatusDeprecated)
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
// 置信度：Bundle 独有的第二根轴」/ 步骤 5 2026-08-13 编注's
// "bundle.RecordMemberOutcome"). No caller exists yet in this phase — Bundle's
// own bundle_success/bundle_failure trigger-axis signal writing (阶段 2) is
// out of scope — this is scaffolding for that future consumer, written
// complete and correct even though currently unreferenced. A pointID not
// found among the bundle's Members is a no-op (not an error): a member may
// have already been filtered out of a later显影扫描 pass before this outcome
// was recorded.
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
