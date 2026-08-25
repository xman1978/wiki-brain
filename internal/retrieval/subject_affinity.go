package retrieval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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

// RecordSourceAffinitySuccess upserts (domainID, subjectNorm, sourceID) with
// consecutive_failures reset to 0 — called when Trace confirms a full-path
// answer confidently cited evidence from sourceID under this subject
// (creates the binding on first confirmation; no confidence threshold).
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
// 默认关闭): when a trusted-enough binding exists for this (domain,
// normalized subject), skip Step2 (domainPreFilter) and Step3
// (sourceSemanticFilter) — both LLM calls — and recall straight from the
// bound source_id set. Only attempted when the domain is already resolved
// (qc.DomainResolved) — an unresolved domain would need domainPreFilter's own
// LLM domain match to even know which domain(s) to look the binding up
// under, so there is nothing to skip in that case.
//
// Returns ok=false whenever the shortcut doesn't apply or doesn't pan out —
// callers fall through to the normal domainPreFilter→filterAndRecall
// pipeline unchanged. A hit that comes back with empty evidence records one
// circuit-breaker failure (RecordSourceAffinityFailure) before falling
// through, rather than returning the empty result — this is a routing
// optimization, not a source of truth, so it must never be the reason a
// question that the full pipeline could have answered comes back empty.
// Success is deliberately not recorded here — creating/reinforcing a binding
// requires Trace's post-answer confirmation that evidence was actually
// cited (RecordSourceAffinityOutcome), not just "recall returned something
// non-empty" (rerank/judge can still be wrong).
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
// source_ids its DirectPointIDs' evidence actually came from. Sources can
// span more than one domain in principle, so sourceIDs are grouped by their
// own domain_id and normalized/written per domain rather than assuming a
// single caller-supplied domain. Non-fatal on lookup/normalize failure for
// an individual source — logs and continues with the rest.
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
	for domainID, sids := range bySourceDomain {
		subjectNorm, err := s.subjectNormalizer.Normalize(ctx, []string{domainID}, subject)
		if err != nil {
			slog.Warn("retrieval: source affinity subject normalize failed", "domain_id", domainID, "error", err)
			continue
		}
		for _, sid := range sids {
			if err := s.store.RecordSourceAffinitySuccess(domainID, subjectNorm, sid); err != nil {
				slog.Warn("retrieval: record source affinity success failed", "domain_id", domainID, "source_id", sid, "error", err)
			}
		}
	}
	return nil
}
