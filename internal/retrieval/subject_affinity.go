package retrieval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// SubjectNorm is one row of subject_norms (migration 068) — a canonical
// subject that later, wording-jittered extractions normalize onto. Unlike
// activation.QuestionTupleNorm (four fields, must all align to match), this
// only tracks subject — the same real-world topic gets asked about with many
// different intents, and source_affinity only needs to know "which file",
// not "which passage" (that's outline/FTS/judge's job downstream).
type SubjectNorm struct {
	NormID    string
	DomainID  string
	Subject   string
	LastHitAt time.Time
	CreatedAt time.Time
}

// SubjectNormConfig mirrors activation.TupleNormConfig's pattern.
type SubjectNormConfig struct {
	LocalSimMin float64
}

func (c SubjectNormConfig) withDefaults() SubjectNormConfig {
	if c.LocalSimMin <= 0 {
		c.LocalSimMin = 0.8
	}
	return c
}

// SubjectNormalizer runs the same tiered normalization shape as
// activation.TupleNormalizer (exact match → local Jaccard → LLM batch
// judgment → new canonical record), scoped to the subject field alone.
type SubjectNormalizer struct {
	store     *Store
	llmClient llm.LLMClient
	cfg       SubjectNormConfig
}

func NewSubjectNormalizer(store *Store, cfg SubjectNormConfig) *SubjectNormalizer {
	return &SubjectNormalizer{store: store, cfg: cfg.withDefaults()}
}

func (n *SubjectNormalizer) SetLLMClient(c llm.LLMClient) {
	n.llmClient = c
}

// Normalize returns the canonical subject for (domainIDs, subject), running
// the tiers in order and returning as soon as one hits. domainIDs empty ⇒
// nothing to scope the lookup to, returns the plain normalized subject
// unchanged (mirrors TupleNormalizer.Normalize's same guard).
func (n *SubjectNormalizer) Normalize(ctx context.Context, domainIDs []string, subject string) (string, error) {
	qSubject := text.Normalize(subject)
	if len(domainIDs) == 0 || qSubject == "" {
		return qSubject, nil
	}

	// Tier 1: exact match.
	exact, err := n.store.FindExactSubjectNorm(domainIDs, qSubject)
	if err != nil {
		return "", fmt.Errorf("retrieval: subject norm tier1: %w", err)
	}
	if exact != nil {
		if err := n.store.TouchSubjectNormLastHit(exact.NormID); err != nil {
			slog.Warn("retrieval: subject norm touch last hit failed", "norm_id", exact.NormID, "error", err)
		}
		return exact.Subject, nil
	}

	candidates, err := n.store.ListSubjectNormCandidates(domainIDs, 200)
	if err != nil {
		return "", fmt.Errorf("retrieval: subject norm list candidates: %w", err)
	}

	// Tier 2: local token-Jaccard similarity.
	if len(candidates) > 0 {
		bestIdx := -1
		bestScore := 0.0
		for i, c := range candidates {
			score := subjectJaccard(qSubject, c.Subject)
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx >= 0 && bestScore >= n.cfg.LocalSimMin {
			match := candidates[bestIdx]
			if err := n.store.TouchSubjectNormLastHit(match.NormID); err != nil {
				slog.Warn("retrieval: subject norm touch last hit failed", "norm_id", match.NormID, "error", err)
			}
			return match.Subject, nil
		}
	}

	// Tier 3: LLM batch judgment over the candidate set.
	if n.llmClient != nil && len(candidates) > 0 {
		matched, idx, err := n.judgeLLM(ctx, qSubject, candidates)
		if err != nil {
			slog.Warn("retrieval: subject norm LLM tier failed, falling through to new record", "error", err)
		} else if matched && idx >= 0 && idx < len(candidates) {
			match := candidates[idx]
			if err := n.store.TouchSubjectNormLastHit(match.NormID); err != nil {
				slog.Warn("retrieval: subject norm touch last hit failed", "norm_id", match.NormID, "error", err)
			}
			return match.Subject, nil
		}
	}

	// Tier 4: no tier matched — this becomes the new canonical subject, one
	// row per domain (mirrors TupleNormalizer.insertNew).
	now := time.Now().UTC()
	for _, d := range domainIDs {
		norm := &SubjectNorm{DomainID: d, Subject: qSubject, LastHitAt: now, CreatedAt: now}
		if err := n.store.InsertSubjectNorm(norm); err != nil {
			return "", fmt.Errorf("retrieval: subject norm insert new record: %w", err)
		}
	}
	return qSubject, nil
}

// subjectJaccard tokenizes a/b via text.TermSet before comparing — same
// algorithm as activation's unexported jaccard(), reimplemented here since
// it isn't exported across packages.
func subjectJaccard(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	setA := text.TermSet(a)
	setB := text.TermSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1
	}
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	inter := 0
	for w := range setA {
		if _, ok := setB[w]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

type subjectNormLLMCandidate struct {
	Index   int    `json:"index"`
	Subject string `json:"subject"`
}

type subjectNormLLMResult struct {
	Matched        bool `json:"matched"`
	CandidateIndex int  `json:"candidate_index"`
}

func (n *SubjectNormalizer) judgeLLM(ctx context.Context, subject string, candidates []SubjectNorm) (bool, int, error) {
	cands := make([]subjectNormLLMCandidate, len(candidates))
	for i, c := range candidates {
		cands[i] = subjectNormLLMCandidate{Index: i, Subject: c.Subject}
	}
	candsJSON, err := json.Marshal(cands)
	if err != nil {
		return false, -1, fmt.Errorf("marshal candidates: %w", err)
	}
	vars := map[string]string{
		"subject":    subject,
		"candidates": string(candsJSON),
	}
	raw, err := n.llmClient.CompleteJSON(ctx, "subject_norm_match.md", vars, "classification")
	if err != nil {
		return false, -1, fmt.Errorf("subject_norm_match completion: %w", err)
	}
	var result subjectNormLLMResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, -1, fmt.Errorf("unmarshal subject_norm_match result: %w", err)
	}
	if !result.Matched {
		return false, -1, nil
	}
	return true, result.CandidateIndex, nil
}

// ---- subject_norms store methods ----

const subjectNormColumns = `norm_id, domain_id, subject, last_hit_at, created_at`

func scanSubjectNorm(row interface{ Scan(...interface{}) error }) (*SubjectNorm, error) {
	var n SubjectNorm
	if err := row.Scan(&n.NormID, &n.DomainID, &n.Subject, &n.LastHitAt, &n.CreatedAt); err != nil {
		return nil, err
	}
	return &n, nil
}

// FindExactSubjectNorm is Tier 1: an exact string match on the normalized
// subject, scoped to any of domainIDs.
func (s *Store) FindExactSubjectNorm(domainIDs []string, subject string) (*SubjectNorm, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(domainIDs)
	args = append(args, subject)

	query := fmt.Sprintf(`SELECT %s FROM subject_norms
		WHERE domain_id IN (%s) AND subject = ?
		ORDER BY last_hit_at DESC LIMIT 1`, subjectNormColumns, ph)
	row := s.db.QueryRow(query, args...)
	n, err := scanSubjectNorm(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("retrieval store: find exact subject norm match: %w", err)
	}
	return n, nil
}

// ListSubjectNormCandidates returns up to limit rows across domainIDs, most
// recently hit first — the candidate pool for Tier 2/3.
func (s *Store) ListSubjectNormCandidates(domainIDs []string, limit int) ([]SubjectNorm, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	ph, args := buildPlaceholders(domainIDs)
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT %s FROM subject_norms
		WHERE domain_id IN (%s) ORDER BY last_hit_at DESC LIMIT ?`, subjectNormColumns, ph)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list subject norm candidates: %w", err)
	}
	defer rows.Close()

	var out []SubjectNorm
	for rows.Next() {
		n, err := scanSubjectNorm(rows)
		if err != nil {
			return nil, fmt.Errorf("retrieval store: scan subject norm candidate: %w", err)
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// InsertSubjectNorm writes a new subject_norms row.
func (s *Store) InsertSubjectNorm(n *SubjectNorm) error {
	if n.NormID == "" {
		n.NormID = uuid.NewString()
	}
	now := time.Now().UTC()
	if n.LastHitAt.IsZero() {
		n.LastHitAt = now
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	_, err := s.db.Exec(`INSERT INTO subject_norms (norm_id, domain_id, subject, last_hit_at, created_at)
		VALUES (?, ?, ?, ?, ?)`, n.NormID, n.DomainID, n.Subject, n.LastHitAt, n.CreatedAt)
	if err != nil {
		return fmt.Errorf("retrieval store: insert subject norm: %w", err)
	}
	return nil
}

// TouchSubjectNormLastHit bumps last_hit_at to now.
func (s *Store) TouchSubjectNormLastHit(normID string) error {
	_, err := s.db.Exec(`UPDATE subject_norms SET last_hit_at = ? WHERE norm_id = ?`, time.Now().UTC(), normID)
	if err != nil {
		return fmt.Errorf("retrieval store: touch subject norm last_hit_at: %w", err)
	}
	return nil
}

// DeleteIdleSubjectNorms removes rows whose last_hit_at is older than days
// ago — same idle-cleanup convention as activation.Store.DeleteIdleOlderThan
// (question_tuple_norms). Wired into Study's periodic housekeeping loop via
// Service.CleanIdleSubjectNorms below (study.Service holds a
// *retrieval.Service for this and CleanIdleSourceAffinity specifically).
func (s *Store) DeleteIdleSubjectNorms(days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	res, err := s.db.Exec(`DELETE FROM subject_norms WHERE last_hit_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("retrieval store: delete idle subject norms: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retrieval store: delete idle subject norms rows affected: %w", err)
	}
	return int(n), nil
}

// ---- source_affinity store methods ----

// GetSourceAffinitySources returns the source_ids bound to (domainIDs,
// subjectNorm) — the candidate set retrieveSlowPath uses to skip
// domainPreFilter/sourceSemanticFilter when non-empty.
func (s *Store) GetSourceAffinitySources(domainIDs []string, subjectNorm string) ([]string, error) {
	if len(domainIDs) == 0 || subjectNorm == "" {
		return nil, nil
	}
	ph, args := buildPlaceholders(domainIDs)
	args = append(args, subjectNorm)

	query := fmt.Sprintf(`SELECT DISTINCT source_id FROM source_affinity
		WHERE domain_id IN (%s) AND subject_norm = ?`, ph)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get source affinity sources: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			return nil, fmt.Errorf("retrieval store: scan source affinity source: %w", err)
		}
		out = append(out, sourceID)
	}
	return out, rows.Err()
}

// SourceAffinityBinding is one source_affinity row, keyed from the source
// side (as opposed to GetSourceAffinitySources, keyed from the subject
// side) — the shape the source-detail "主题标签" management panel reads and
// writes through (ListSourceAffinityBySourceID/DeleteSourceAffinityByID).
type SourceAffinityBinding struct {
	AffinityID  string `json:"affinity_id"`
	DomainID    string `json:"domain_id"`
	SubjectNorm string `json:"subject_norm"`
}

// ListSourceAffinityBySourceID returns every tag bound to sourceID, ordered
// by subject_norm — the reverse direction of GetSourceAffinitySources, for
// the source-detail page's manual tag management (list/add/edit/delete).
func (s *Store) ListSourceAffinityBySourceID(sourceID string) ([]SourceAffinityBinding, error) {
	if sourceID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT affinity_id, domain_id, subject_norm FROM source_affinity
		WHERE source_id = ? ORDER BY subject_norm`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list source affinity by source id: %w", err)
	}
	defer rows.Close()

	var out []SourceAffinityBinding
	for rows.Next() {
		var b SourceAffinityBinding
		if err := rows.Scan(&b.AffinityID, &b.DomainID, &b.SubjectNorm); err != nil {
			return nil, fmt.Errorf("retrieval store: scan source affinity binding: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteSourceAffinityByID removes a single binding by its affinity_id — the
// source-detail page's manual "delete tag" action. No-op (not an error) if
// the id doesn't exist.
func (s *Store) DeleteSourceAffinityByID(affinityID string) error {
	if _, err := s.db.Exec(`DELETE FROM source_affinity WHERE affinity_id = ?`, affinityID); err != nil {
		return fmt.Errorf("retrieval store: delete source affinity by id: %w", err)
	}
	return nil
}

// RecordSourceAffinitySuccess upserts (domainID, subjectNorm, sourceID) with
// consecutive_failures reset to 0 — called both by the pending-subject
// background matcher (ProcessPendingSubjectMatches) when a queued subject's
// sourceSemanticFilter pass matches sourceID, and by the proactive re-tagging
// path (BackfillSourceAffinityForSource) when a newly-ready source
// semantically matches an existing subject tag. Both write paths run the
// same kind of judgment (a source_filter.md-equivalent semantic match), so
// there is a single trust tier — no verification step is skipped at query
// time that either write path itself hasn't already performed.
func (s *Store) RecordSourceAffinitySuccess(domainID, subjectNorm, sourceID string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO source_affinity
		(affinity_id, domain_id, subject_norm, source_id, consecutive_failures, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(domain_id, subject_norm, source_id)
		DO UPDATE SET consecutive_failures = 0, updated_at = excluded.updated_at`,
		uuid.NewString(), domainID, subjectNorm, sourceID, now, now)
	if err != nil {
		return fmt.Errorf("retrieval store: record source affinity success: %w", err)
	}
	return nil
}

// ListAllSubjectNorms returns every subject_norms row across domainIDs, with
// no recency cap — the domain's full tag vocabulary. Deliberately does NOT
// reuse ListSubjectNormCandidates: that method's 200-row/most-recently-hit
// cap is tuned for the hot per-query Tier2 normalization path, where a
// small, recent candidate pool is the right trade-off. Backfill
// (BackfillSourceAffinityForSource) runs once per newly-ready source, off
// the query hot path, and an idle tag is not necessarily a bad tag — it may
// be idle precisely because no source was ever bound to it yet, which is
// exactly the gap backfill exists to close. Reusing the capped/recency-
// ordered method here would silently skip cold tags, defeating the purpose.
func (s *Store) ListAllSubjectNorms(domainIDs []string) ([]SubjectNorm, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(domainIDs)
	query := fmt.Sprintf(`SELECT %s FROM subject_norms WHERE domain_id IN (%s)`, subjectNormColumns, ph)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list all subject norms: %w", err)
	}
	defer rows.Close()

	var out []SubjectNorm
	for rows.Next() {
		n, err := scanSubjectNorm(rows)
		if err != nil {
			return nil, fmt.Errorf("retrieval store: scan subject norm: %w", err)
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// RecordSourceAffinityFailure increments consecutive_failures for every
// existing (domainID, subjectNorm, sourceID) row among sourceIDs — a
// circuit breaker, not a Beta posterior: once consecutive_failures reaches
// maxFailures the row is deleted outright (no partial credit for past
// successes), so a binding that stops working self-heals by disappearing
// rather than accumulating a probability score. Rows are only touched, never
// created, here — a source that was never bound has nothing to weaken.
func (s *Store) RecordSourceAffinityFailure(domainID, subjectNorm string, sourceIDs []string, maxFailures int) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	if maxFailures <= 0 {
		maxFailures = 2
	}
	now := time.Now().UTC()
	ph, args := buildPlaceholders(sourceIDs)
	args = append([]interface{}{now, domainID, subjectNorm}, args...)
	query := fmt.Sprintf(`UPDATE source_affinity SET consecutive_failures = consecutive_failures + 1, updated_at = ?
		WHERE domain_id = ? AND subject_norm = ? AND source_id IN (%s)`, ph)
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("retrieval store: record source affinity failure: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM source_affinity WHERE domain_id = ? AND subject_norm = ? AND consecutive_failures >= ?`,
		domainID, subjectNorm, maxFailures); err != nil {
		return fmt.Errorf("retrieval store: evict failed source affinity: %w", err)
	}
	return nil
}

// DeleteIdleSourceAffinity removes rows whose updated_at is older than days
// ago.
func (s *Store) DeleteIdleSourceAffinity(days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	res, err := s.db.Exec(`DELETE FROM source_affinity WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("retrieval store: delete idle source affinity: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retrieval store: delete idle source affinity rows affected: %w", err)
	}
	return int(n), nil
}

// CleanIdleSubjectNorms/CleanIdleSourceAffinity are Study's periodic
// housekeeping passthroughs (docs/impl/v1/study.md 步骤 4 同款 idle 清理惯例,
// mirroring activation.Service.CleanIdleTupleNorms) — study.Service calls
// these directly rather than through the SourceAffinityWriter interface
// (that one is Trace's write path; this is a different, unrelated caller).
func (s *Service) CleanIdleSubjectNorms(days int) (int, error) {
	return s.store.DeleteIdleSubjectNorms(days)
}

func (s *Service) CleanIdleSourceAffinity(days int) (int, error) {
	return s.store.DeleteIdleSourceAffinity(days)
}

// ---- manual tag management (source-detail page "主题标签" panel) ----

// ListSourceSubjectTags lists the tags currently bound to sourceID.
func (s *Service) ListSourceSubjectTags(sourceID string) ([]SourceAffinityBinding, error) {
	return s.store.ListSourceAffinityBySourceID(sourceID)
}

// ErrSourceHasNoDomain is returned by AddSourceSubjectTag when sourceID
// isn't classified into any domain — subject_norms/source_affinity are
// domain-scoped by design, so there is nowhere to normalize/bind a tag
// under.
var ErrSourceHasNoDomain = errors.New("retrieval: source has no domain, cannot add a subject tag")

// AddSourceSubjectTag manually binds subjectText to sourceID — the
// source-detail page's "add tag" action. Goes through the same
// SubjectNormalizer tiers as every other write path (never lets a manually
// typed tag bypass normalization and drift out of the canonical subject_norm
// vocabulary), then the same RecordSourceAffinitySuccess every other write
// path uses — a manually added tag is trusted exactly as much as one the
// background matcher found.
func (s *Service) AddSourceSubjectTag(ctx context.Context, sourceID, subjectText string) (*SourceAffinityBinding, error) {
	if sourceID == "" || strings.TrimSpace(subjectText) == "" {
		return nil, fmt.Errorf("retrieval: source id and subject text are required")
	}
	domainID, err := s.store.SourceDomainID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("retrieval: add source subject tag domain lookup: %w", err)
	}
	if domainID == "" {
		return nil, ErrSourceHasNoDomain
	}
	subjectNorm, err := s.subjectNormalizer.Normalize(ctx, []string{domainID}, subjectText)
	if err != nil {
		return nil, fmt.Errorf("retrieval: add source subject tag normalize: %w", err)
	}
	if err := s.store.RecordSourceAffinitySuccess(domainID, subjectNorm, sourceID); err != nil {
		return nil, fmt.Errorf("retrieval: add source subject tag record: %w", err)
	}
	bindings, err := s.store.ListSourceAffinityBySourceID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("retrieval: add source subject tag lookup: %w", err)
	}
	for _, b := range bindings {
		if b.DomainID == domainID && b.SubjectNorm == subjectNorm {
			return &b, nil
		}
	}
	return &SourceAffinityBinding{DomainID: domainID, SubjectNorm: subjectNorm}, nil
}

// RemoveSourceSubjectTag removes a single binding by affinity_id — the
// source-detail page's "delete tag" action.
func (s *Service) RemoveSourceSubjectTag(affinityID string) error {
	return s.store.DeleteSourceAffinityByID(affinityID)
}

// ---- pending_subject_affinity_match store methods (migration 072) ----

// PendingSubjectMatch is one row of pending_subject_affinity_match — a
// (domain, normalized subject) pair a confident full-path answer touched,
// queued for ProcessPendingSubjectMatches to run a full sourceSemanticFilter
// pass against, not yet processed.
type PendingSubjectMatch struct {
	DomainID    string
	SubjectNorm string
	QueuedAt    time.Time
}

// EnqueuePendingSubjectMatch queues (domainID, subjectNorm) for background
// matching — called by RecordSourceAffinityOutcome. ON CONFLICT DO NOTHING:
// the same subject asked many times before it's processed still occupies
// exactly one row, preserving the earliest queued_at (FIFO fairness) rather
// than being pushed to the back of the queue on every repeat ask.
func (s *Store) EnqueuePendingSubjectMatch(domainID, subjectNorm string) error {
	_, err := s.db.Exec(`INSERT INTO pending_subject_affinity_match (domain_id, subject_norm, queued_at)
		VALUES (?, ?, ?)
		ON CONFLICT(domain_id, subject_norm) DO NOTHING`,
		domainID, subjectNorm, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("retrieval store: enqueue pending subject match: %w", err)
	}
	return nil
}

// ListPendingSubjectMatches returns up to limit queued rows, oldest first.
func (s *Store) ListPendingSubjectMatches(limit int) ([]PendingSubjectMatch, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT domain_id, subject_norm, queued_at FROM pending_subject_affinity_match
		ORDER BY queued_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list pending subject matches: %w", err)
	}
	defer rows.Close()

	var out []PendingSubjectMatch
	for rows.Next() {
		var p PendingSubjectMatch
		if err := rows.Scan(&p.DomainID, &p.SubjectNorm, &p.QueuedAt); err != nil {
			return nil, fmt.Errorf("retrieval store: scan pending subject match: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePendingSubjectMatch removes a queued entry once ProcessPendingSubjectMatches
// has handled it (matched or not — this is a one-shot task, not a retry queue;
// the subject gets re-queued next time a confident answer touches it).
func (s *Store) DeletePendingSubjectMatch(domainID, subjectNorm string) error {
	_, err := s.db.Exec(`DELETE FROM pending_subject_affinity_match WHERE domain_id = ? AND subject_norm = ?`,
		domainID, subjectNorm)
	if err != nil {
		return fmt.Errorf("retrieval store: delete pending subject match: %w", err)
	}
	return nil
}

// sourceAffinityPendingBatchMax resolves retrieval.source_affinity_pending_batch_max
// with a default, mirroring sourceAffinityFailureMax's pattern.
func (s *Service) sourceAffinityPendingBatchMax() int {
	if s.cfg != nil && s.cfg.Retrieval.SourceAffinityPendingBatchMax > 0 {
		return s.cfg.Retrieval.SourceAffinityPendingBatchMax
	}
	return 50
}

// ProcessPendingSubjectMatches is Study's periodic background matcher
// (docs/design/retrieval.md 第 14 节，2026-08-27 改判): for each queued
// (domain, normalized subject) pair, run a full sourceSemanticFilter pass
// against every source in that domain — the exact same title/summary
// relevance judgment sourceSemanticFilter already applies at query time
// (source_filter.md), just moved off the request path — and bind whatever
// matches. Processes up to sourceAffinityPendingBatchMax entries per call so
// a domain that accumulates many distinct subjects between Study ticks
// doesn't make a single tick's duration unbounded; the rest are picked up
// next tick. Always dequeues a processed entry regardless of match outcome
// — this is a one-shot task, not a retry queue (see DeletePendingSubjectMatch).
func (s *Service) ProcessPendingSubjectMatches(ctx context.Context) (int, error) {
	if !s.sourceAffinityEnabled() {
		return 0, nil
	}
	pending, err := s.store.ListPendingSubjectMatches(s.sourceAffinityPendingBatchMax())
	if err != nil {
		return 0, fmt.Errorf("retrieval: list pending subject matches: %w", err)
	}

	processed := 0
	for _, p := range pending {
		sources, err := s.store.ListSourcesByDomainIDs([]string{p.DomainID})
		if err != nil {
			slog.Warn("retrieval: pending subject match source lookup failed", "domain_id", p.DomainID, "subject_norm", p.SubjectNorm, "error", err)
		} else if len(sources) > 0 {
			qc := QueryContext{Subject: p.SubjectNorm, DomainIDs: []string{p.DomainID}}
			matched, ferr := s.sourceSemanticFilter(ctx, qc, sources)
			if ferr != nil {
				slog.Warn("retrieval: pending subject match source filter failed", "domain_id", p.DomainID, "subject_norm", p.SubjectNorm, "error", ferr)
			}
			for _, src := range matched {
				if err := s.store.RecordSourceAffinitySuccess(p.DomainID, p.SubjectNorm, src.SourceID); err != nil {
					slog.Warn("retrieval: record source affinity success failed", "domain_id", p.DomainID, "subject_norm", p.SubjectNorm, "source_id", src.SourceID, "error", err)
				}
			}
		}
		if err := s.store.DeletePendingSubjectMatch(p.DomainID, p.SubjectNorm); err != nil {
			slog.Warn("retrieval: delete pending subject match failed", "domain_id", p.DomainID, "subject_norm", p.SubjectNorm, "error", err)
		}
		processed++
	}
	return processed, nil
}

// GetSourcesByIDs fetches SourceInfo rows for exactly the given source_ids —
// used by retrieveSlowPath's affinity shortcut to turn a bound source_id set
// back into the []SourceInfo shape recallFromSources expects.
func (s *Store) GetSourcesByIDs(sourceIDs []string) ([]SourceInfo, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(sourceIDs)
	query := fmt.Sprintf(`SELECT source_id, title, summary, domain_id, markdown_path FROM sources
		WHERE source_id IN (%s) AND shadow_of IS NULL`, ph)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get sources by ids: %w", err)
	}
	defer rows.Close()

	var out []SourceInfo
	for rows.Next() {
		var src SourceInfo
		if err := rows.Scan(&src.SourceID, &src.Title, &src.Summary, &src.DomainID, &src.MarkdownPath); err != nil {
			return nil, fmt.Errorf("retrieval store: scan source by id: %w", err)
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// SourceDomainID returns sourceID's domain_id ("" when the source has no
// domain) — used by RecordSourceAffinityOutcome (Trace's write path) to
// group cited sources by domain before normalizing/writing per domain.
func (s *Store) SourceDomainID(sourceID string) (string, error) {
	var domainID sql.NullString
	err := s.db.QueryRow(`SELECT domain_id FROM sources WHERE source_id = ?`, sourceID).Scan(&domainID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("retrieval store: get source domain id: %w", err)
	}
	return domainID.String, nil
}

// sourceAffinityConfig resolves cfg.Retrieval's source_affinity_* knobs with
// defaults, mirroring rerankJudgeBatchMaxChars's small-helper pattern.
func (s *Service) sourceAffinityEnabled() bool {
	return s.cfg != nil && s.cfg.Retrieval.SourceAffinityEnabled
}

func (s *Service) sourceAffinityFailureMax() int {
	if s.cfg != nil && s.cfg.Retrieval.SourceAffinityFailureMax > 0 {
		return s.cfg.Retrieval.SourceAffinityFailureMax
	}
	return 2
}

// trySourceAffinityShortcut implements retrieveSlowPath's subject→source
// routing shortcut (2026-08-25, config.Retrieval.SourceAffinityEnabled 门控,
// 默认关闭): when a binding exists for this (domain, normalized subject),
// skip Step2 (domainPreFilter) and Step3 (sourceSemanticFilter) — both LLM
// calls — and recall straight from the bound source_id set. Only attempted
// when the domain is already resolved (qc.DomainResolved) — an unresolved
// domain would need domainPreFilter's own LLM domain match to even know
// which domain(s) to look the binding up under, so there is nothing to skip
// in that case.
//
// Returns ok=false whenever the shortcut doesn't apply or doesn't pan out —
// callers fall through to the normal domainPreFilter→filterAndRecall
// pipeline unchanged. A hit that comes back with empty evidence records one
// circuit-breaker failure (RecordSourceAffinityFailure) before falling
// through, rather than returning the empty result — this is a routing
// optimization, not a source of truth, so it must never be the reason a
// question that the full pipeline could have answered comes back empty.
// Every binding here was itself produced by a full sourceSemanticFilter-
// equivalent match (either the pending-subject background matcher or the
// proactive source re-tagging path, subject_affinity.go/
// source_affinity_backfill.go) — there is a single trust tier, so a hit
// skips both filters unconditionally; nothing is verified again here that
// wasn't already verified when the binding was written (2026-08-27 改判，
// 取代此前 migration 070 引入的 origin 信任分级).
func (s *Service) trySourceAffinityShortcut(ctx context.Context, qc QueryContext, emit func(phase, status, detail string, dur int64), progress ProgressFunc) (*EvidenceSet, bool) {
	if !s.sourceAffinityEnabled() || qc.Subject == "" || !qc.DomainResolved || len(qc.DomainIDs) == 0 {
		return nil, false
	}

	subjectNorm, err := s.subjectNormalizer.Normalize(ctx, qc.DomainIDs, qc.Subject)
	if err != nil {
		slog.Warn("retrieval: source affinity subject normalize failed", "error", err)
		return nil, false
	}

	sourceIDs, err := s.store.GetSourceAffinitySources(qc.DomainIDs, subjectNorm)
	if err != nil {
		slog.Warn("retrieval: source affinity lookup failed", "error", err)
		return nil, false
	}
	if len(sourceIDs) == 0 {
		return nil, false
	}

	sources, err := s.store.GetSourcesByIDs(sourceIDs)
	if err != nil {
		slog.Warn("retrieval: source affinity source lookup failed", "error", err)
		return nil, false
	}
	if len(sources) == 0 {
		return nil, false
	}

	slog.Info("retrieval: subject affinity shortcut hit, skipping domain/source filter", "subject_norm", subjectNorm, "source_ids", sourceIDs)
	titles := make([]string, len(sources))
	for i, src := range sources {
		titles[i] = src.Title
	}
	emit("activation", "start", "", 0)
	emit("activation", "done", fmt.Sprintf("已筛选 %d 个文档：%s（主题绑定直达）", len(titles), strings.Join(titles, "、")), 0)
	es, err := s.recallFromSources(ctx, qc, sources, emit, progress, false)
	if err == nil && !evidenceEmpty(es) {
		return es, true
	}
	if err != nil {
		slog.Warn("retrieval: source affinity shortcut recall failed, falling back to full pipeline", "error", err)
	}

	maxFailures := s.sourceAffinityFailureMax()
	for _, domainID := range qc.DomainIDs {
		if ferr := s.store.RecordSourceAffinityFailure(domainID, subjectNorm, sourceIDs, maxFailures); ferr != nil {
			slog.Warn("retrieval: record source affinity failure failed", "domain_id", domainID, "error", ferr)
		}
	}
	return nil, false
}

// RecordSourceAffinityOutcome implements trace.SourceAffinityWriter — Trace
// calls this after a full-path answer is graded confident, passing the
// source_ids its DirectPointIDs' evidence actually came from — used here
// only to resolve which domain(s) this subject belongs to, not to decide
// which sources get bound (2026-08-27 改判，取代此前"直接把这次答案引用到
// 的 source 绑定到主题上"的口径). Binding only the sources one particular
// answer happened to cite under-covers the subject — a differently-scoped
// later question asked under the same or a normalized-equal subject would
// inherit that narrow set instead of getting its own complete match. Instead,
// for each domain this subject touches, the normalized subject is enqueued
// (EnqueuePendingSubjectMatch) for Study's periodic background matcher
// (ProcessPendingSubjectMatches) to run a full sourceSemanticFilter pass
// against every source in that domain — the same rigor sourceSemanticFilter
// already applies at query time, just moved off the request path. Sources
// can span more than one domain in principle, so sourceIDs are still grouped
// by their own domain_id first. Non-fatal on lookup/normalize failure for an
// individual source — logs and continues with the rest.
//
// No context parameter (mirrors activation.Service.EnrichFromConfidentFullPath,
// the other ObservedConditionEnricher-style write path Trace calls): Trace's
// ProcessTrace itself has none (queue-consumer entry point), so this builds
// its own bounded context internally, same as Trace's own resolveDirectEvidence.
func (s *Service) RecordSourceAffinityOutcome(subject string, sourceIDs []string) error {
	if !s.sourceAffinityEnabled() || subject == "" || len(sourceIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	domainIDs := make(map[string]bool)
	for _, sid := range sourceIDs {
		if sid == "" {
			continue
		}
		domainID, err := s.store.SourceDomainID(sid)
		if err != nil {
			slog.Warn("retrieval: source affinity domain lookup failed", "source_id", sid, "error", err)
			continue
		}
		if domainID == "" {
			continue
		}
		domainIDs[domainID] = true
	}
	for domainID := range domainIDs {
		subjectNorm, err := s.subjectNormalizer.Normalize(ctx, []string{domainID}, subject)
		if err != nil {
			slog.Warn("retrieval: source affinity subject normalize failed", "domain_id", domainID, "error", err)
			continue
		}
		if err := s.store.EnqueuePendingSubjectMatch(domainID, subjectNorm); err != nil {
			slog.Warn("retrieval: enqueue pending subject match failed", "domain_id", domainID, "subject_norm", subjectNorm, "error", err)
		}
	}
	return nil
}

// RecordSourceAffinityFeedbackFailure implements trace.SourceAffinityWriter's
// second write path (会话讨论 2026-08-26): explicit negative user feedback on
// a trace that relied on a source_affinity binding counts as one
// circuit-breaker failure against that binding, same counter/threshold the
// shortcut's own empty-recall failure uses (trySourceAffinityShortcut).
// Mirrors RecordSourceAffinityOutcome's per-domain grouping/normalization,
// but calls RecordSourceAffinityFailure instead of Success.
func (s *Service) RecordSourceAffinityFeedbackFailure(subject string, sourceIDs []string) error {
	if !s.sourceAffinityEnabled() || subject == "" || len(sourceIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bySourceDomain := make(map[string][]string)
	seen := make(map[string]bool, len(sourceIDs))
	for _, sid := range sourceIDs {
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		domainID, err := s.store.SourceDomainID(sid)
		if err != nil {
			slog.Warn("retrieval: source affinity domain lookup failed", "source_id", sid, "error", err)
			continue
		}
		if domainID == "" {
			continue
		}
		bySourceDomain[domainID] = append(bySourceDomain[domainID], sid)
	}
	maxFailures := s.sourceAffinityFailureMax()
	for domainID, sids := range bySourceDomain {
		subjectNorm, err := s.subjectNormalizer.Normalize(ctx, []string{domainID}, subject)
		if err != nil {
			slog.Warn("retrieval: source affinity subject normalize failed", "domain_id", domainID, "error", err)
			continue
		}
		if err := s.store.RecordSourceAffinityFailure(domainID, subjectNorm, sids, maxFailures); err != nil {
			slog.Warn("retrieval: record source affinity feedback failure failed", "domain_id", domainID, "error", err)
		}
	}
	return nil
}
