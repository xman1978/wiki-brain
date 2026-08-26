package activation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jxman78/wiki-brain/internal/session"
)

type Service struct {
	store           *Store
	matcher         *Matcher
	bundleMatcher   *BundleMatcher
	tupleNormalizer *TupleNormalizer
	confidenceCfg   ConfidenceConfig
}

func NewService(store *Store, matcher *Matcher) *Service {
	return &Service{store: store, matcher: matcher, bundleMatcher: NewBundleMatcher(store)}
}

// SetTupleNormalizer wires the question-tuple-normalization orchestrator
// (docs/impl/v1/retrieval.md 步骤 2, tuplenorm.go) — optional, nil by default
// so Service behaves unchanged when the feature is config-gated off.
func (s *Service) SetTupleNormalizer(n *TupleNormalizer) {
	s.tupleNormalizer = n
}

// NormalizeTuple is Retrieval's entry point into the tuple-normalization
// layer (docs/impl/v1/retrieval.md 步骤 2), mirroring Match's passthrough
// shape so Retrieval only depends on this package's public surface.
func (s *Service) NormalizeTuple(ctx context.Context, domainIDs []string, subject, intent, audience, constraint string) (normSubject, normIntent, normAudience, normConstraint, intentRaw, constraintRaw string, err error) {
	if s.tupleNormalizer == nil {
		return "", "", "", "", "", "", fmt.Errorf("activation: tuple normalizer not configured")
	}
	return s.tupleNormalizer.Normalize(ctx, domainIDs, subject, intent, audience, constraint)
}

// CleanIdleTupleNorms is Study's periodic housekeeping passthrough
// (docs/impl/v1/study.md 步骤 4 同款 idle 清理惯例) — deletes
// question_tuple_norms rows whose last_hit_at is older than idleDays.
func (s *Service) CleanIdleTupleNorms(idleDays int) (int, error) {
	return s.store.DeleteIdleOlderThan(idleDays)
}

// BundleMatcher exposes the Bundle matcher for Study's显影扫描
// (bundle_scan.go, docs/impl/v1/activation-bundle.md 步骤 4) — Study needs
// the richer per-match APIs (InvalidateCache after CreateBundle/
// UpdateBundleMembers) that a thin passthrough wrapper would just duplicate.
func (s *Service) BundleMatcher() *BundleMatcher {
	return s.bundleMatcher
}

// MatchBundles is阶段 6 只读 API 和 Study 显影扫描共用的入口 — 阶段 1 没有
// Retrieval 消费者，两者是仅有的调用方。domainIDs scopes the BundleMatcher's
// domain-sharded cache (2026-08-25) — pass nil/empty when the query's domain
// is unresolved (Study's显影扫描 always passes nil, scanning every domain,
// same as before this scoping was added).
func (s *Service) MatchBundles(ctx context.Context, query session.ExpandedQuery, domainIDs []string, cfg MatchConfig) ([]BundleMatch, error) {
	if s.bundleMatcher == nil {
		return nil, nil
	}
	return s.bundleMatcher.Match(ctx, query, domainIDs, cfg)
}

func (s *Service) Store() *Store {
	return s.store
}

// Match is Retrieval's entry point into the activation layer
// (docs/impl/v1/retrieval.md 步骤 2): a thin passthrough to the Matcher so
// Retrieval only needs one dependency on this package. domainIDs scopes the
// Matcher's domain-sharded cache (2026-08-25) — pass nil/empty when the
// query's domain is unresolved.
func (s *Service) Match(ctx context.Context, query session.ExpandedQuery, domainIDs []string, cfg MatchConfig) ([]LinkMatch, error) {
	if s.matcher == nil {
		return nil, nil
	}
	return s.matcher.Match(ctx, query, domainIDs, cfg)
}

// CreateLink is idempotent on point_id: a second call for a point that
// already has a link returns the existing link unchanged (Study's own
// tryCreateLink already checks this before calling in — see
// docs/impl/v1/study.md 步骤 2 — but this check stays here too so CreateLink
// is safe to call directly: idx_al_point_id is a UNIQUE index, so skipping
// this check would surface as a raw SQL constraint error instead of a
// graceful return). If the existing link was deprecated, creation is refused
// rather than reviving it — a deprecated condition needs a human/Study
// decision informed by fresh accumulated signal, not an automatic recreate
// (docs/impl/v1/activation.md 步骤 1).
func (s *Service) CreateLink(questionTerms string, cond LinkCondition, pointID string, createdFrom []string) (*ActivationLink, error) {
	existing, err := s.store.GetByPointID(pointID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Status == StatusDeprecated {
			slog.Info("activation: refusing to recreate deprecated link",
				"link_id", existing.LinkID, "point_id", pointID)
			return nil, fmt.Errorf("activation: link %s for point_id=%s is deprecated; refusing to recreate",
				existing.LinkID, pointID)
		}
		return existing, nil
	}

	createdFromJSON, err := json.Marshal(createdFrom)
	if err != nil {
		return nil, fmt.Errorf("activation: marshal created_from: %w", err)
	}

	conds := cond.EffectiveConditions()
	if conds == nil {
		conds = []ObservedCondition{}
	}
	link := &ActivationLink{
		QuestionTerms:      questionTerms,
		ObservedConditions: conds,
		PointID:            pointID,
		Status:             StatusCandidate,
		CreatedFrom:        string(createdFromJSON),
	}
	applyLegacyProjection(link)
	if err := s.store.InsertLink(link); err != nil {
		return nil, err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return s.store.GetByID(link.LinkID)
}

// AppendObservedCondition merges one quadruple into an existing link (Trace
// slow-path enrichment). max<=0 defaults to 50. Skips deprecated links.
func (s *Service) AppendObservedCondition(linkID string, add ObservedCondition, max int) error {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return err
	}
	if link == nil {
		return fmt.Errorf("activation: link not found: %s", linkID)
	}
	if link.Status == StatusDeprecated {
		return nil
	}
	if err := s.store.AppendObservedCondition(linkID, add, max); err != nil {
		return err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

// ReplaceObservedConditions is Study's full rebuild write path.
func (s *Service) ReplaceObservedConditions(linkID string, conds []ObservedCondition) error {
	if err := s.store.ReplaceObservedConditions(linkID, conds); err != nil {
		return err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

// SetConfidenceConfig wires the shared retrieval.* confidence knobs
// (docs/impl/v1/activation.md 配置项) into the Matcher, BundleMatcher, and
// Store — the three places that need it (Match's tiering, Bundle's tiering,
// and deriveAndPersistBundleStatus). Called once from cmd/server/main.go
// after construction.
func (s *Service) SetConfidenceConfig(cfg ConfidenceConfig) {
	s.confidenceCfg = cfg
	if s.matcher != nil {
		s.matcher.SetConfidenceConfig(cfg)
	}
	if s.bundleMatcher != nil {
		s.bundleMatcher.SetConfidenceConfig(cfg)
	}
	if s.store != nil {
		s.store.SetConfidenceConfig(cfg)
	}
}

// ConfidenceConfig returns the shared retrieval.* confidence knobs wired via
// SetConfidenceConfig — Study's convergence-report aggregation
// (docs/impl/v1/study.md 步骤 7) needs the same tier boundaries Match() uses.
func (s *Service) ConfidenceConfig() ConfidenceConfig {
	return s.confidenceCfg
}

// deriveAndPersistStatus recomputes the candidate/verified derived status
// from link's current ObservedConditions, ALSO checks the target KP's
// lifecycle and forces deprecated regardless of what the condition-based
// derivation says (docs/impl/v1/activation.md「与旧状态机的映射」: deprecated
// ⟺ KP lifecycle != current, independent of confidence). Persists only on
// change. Called from every write path that changes observed_conditions or
// the link's serving eligibility (RecordOutcome/RecordAuditOutcome/
// UpdateConditions/ReplaceObservedConditions/AppendObservedCondition/
// lifecycle notification) — not just the two new entry points — so Wiki's
// status='verified' reads never go stale.
func (s *Service) deriveAndPersistStatus(link *ActivationLink) error {
	if link == nil {
		return nil
	}
	current, err := s.store.PointLifecycleCurrent(link.PointID)
	if err != nil {
		return err
	}
	newStatus := StatusDeprecated
	if current {
		newStatus = deriveStatus(link.ObservedConditions, s.confidenceCfg)
	}
	if newStatus == link.Status {
		return nil
	}
	if err := s.store.UpdateStatus(link.LinkID, newStatus); err != nil {
		return err
	}
	return nil
}

// RecordOutcome implements docs/impl/v1/activation.md 步骤 1: locates the
// matched observed condition by exact four-tuple and increments its success/
// failure count, folds questionTerms into its known_question_terms, keeps
// the link's display adopt_count/fail_count in sync (replacing the old
// UpdateStats call site — Trace now calls this directly instead of Study
// batch-processing signal events), re-derives status, and invalidates the
// Matcher cache. matched=false from the Store (no error) logs a warning and
// returns nil — a missing condition here is unexpected (callers should be
// operating on a condition Match() just returned a hit for) but must never
// abort the caller's trace_write task over a bookkeeping miss.
func (s *Service) RecordOutcome(linkID, subject, intent, audience, constraint string, success bool, questionTerms string) error {
	matched, link, err := s.store.RecordOutcome(linkID, subject, intent, audience, constraint, success, questionTerms)
	if err != nil {
		return err
	}
	if !matched {
		slog.Warn("activation: record outcome found no matching condition", "link_id", linkID,
			"subject", subject, "intent", intent, "audience", audience, "constraint", constraint)
		return nil
	}
	if err := s.deriveAndPersistStatus(link); err != nil {
		return err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

// RecordAuditOutcome mirrors RecordOutcome for independent-verification
// results (docs/impl/v1/activation.md 步骤 1).
func (s *Service) RecordAuditOutcome(linkID, subject, intent, audience, constraint string, agree bool) error {
	matched, link, err := s.store.RecordAuditOutcome(linkID, subject, intent, audience, constraint, agree)
	if err != nil {
		return err
	}
	if !matched {
		slog.Warn("activation: record audit outcome found no matching condition", "link_id", linkID,
			"subject", subject, "intent", intent, "audience", audience, "constraint", constraint)
		return nil
	}
	if err := s.deriveAndPersistStatus(link); err != nil {
		return err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

// RecordBundleOutcome mirrors RecordOutcome for a Bundle's trigger-axis
// condition (docs/impl/v1/activation-bundle.md「验证」, 2026-08-20 阶段 2
// 接线) — trace.recordBundleHitOutcome calls this once per Bundle hit.
// Store.RecordBundleOutcome already re-derives and persists the trigger-axis
// status as part of its write (via UpdateBundleMembers →
// deriveAndPersistBundleStatus), unlike Link's Service.RecordOutcome there's
// no separate lifecycle re-check here — Bundle deprecation stays
// lifecycle-sweep-driven (study.weakenBundlesWithExpiredCoreMembers), not a
// per-write concern. matched=false (no error) logs a warning and returns nil,
// same non-fatal contract as RecordOutcome.
func (s *Service) RecordBundleOutcome(bundleID, subject, intent, audience, constraint string, success bool) error {
	matched, _, err := s.store.RecordBundleOutcome(bundleID, subject, intent, audience, constraint, success)
	if err != nil {
		return err
	}
	if !matched {
		slog.Warn("activation: record bundle outcome found no matching condition", "bundle_id", bundleID,
			"subject", subject, "intent", intent, "audience", audience, "constraint", constraint)
		return nil
	}
	if s.bundleMatcher != nil {
		s.bundleMatcher.InvalidateCache()
	}
	return nil
}

// RecordMemberOutcome mirrors RecordBundleOutcome for a Bundle's member axis
// (docs/impl/v1/activation-bundle.md「成员置信度」) — trace.recordBundleHitOutcome
// calls this once per member point actually used to serve a Bundle hit.
// Store.RecordMemberOutcome no-ops (not an error) when pointID isn't among
// the bundle's current members, so this passthrough has nothing extra to
// check before invalidating the match cache.
func (s *Service) RecordMemberOutcome(bundleID, pointID string, success bool) error {
	if err := s.store.RecordMemberOutcome(bundleID, pointID, success); err != nil {
		return err
	}
	if s.bundleMatcher != nil {
		s.bundleMatcher.InvalidateCache()
	}
	return nil
}

// NotifyPointsLifecycleChanged implements the extended unit.ActivationNotifier
// interface (docs/impl/v1/activation.md「依赖」Lifecycle): for each pointID
// with a non-deprecated existing link, re-derive status — deriveAndPersistStatus's
// own lifecycle check handles both directions (KP went non-current → link
// becomes deprecated; KP restored → link re-derives from its conditions)
// through the same single code path.
func (s *Service) NotifyPointsLifecycleChanged(pointIDs []string) error {
	for _, pid := range pointIDs {
		link, err := s.store.GetByPointID(pid)
		if err != nil {
			return err
		}
		if link == nil || link.Status == StatusDeprecated {
			continue
		}
		if err := s.deriveAndPersistStatus(link); err != nil {
			return err
		}
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

func (s *Service) UpdateStats(linkID string, adoptDelta, failDelta int) error {
	return s.store.UpdateStats(linkID, adoptDelta, failDelta)
}

func (s *Service) TouchLastUsed(linkIDs []string) error {
	return s.store.TouchLastUsed(linkIDs)
}

func (s *Service) GetLink(linkID string) (*ActivationLink, error) {
	return s.store.GetByID(linkID)
}

func (s *Service) ListLinks(f ListLinksFilter) ([]ActivationLinkListRow, error) {
	return s.store.ListLinks(f)
}

func (s *Service) ListLearningResults(linkID string) ([]LearningResult, error) {
	return s.store.ListLearningResultsByObject(ObjectTypeActivationLink, linkID)
}

// LinkQuestions groups create-time fuel and Match-hit questions for the
// detail dialog's lazy 问法列表.
type LinkQuestions struct {
	Matched     []LinkQuestion `json:"matched"`
	CreatedFrom []LinkQuestion `json:"created_from"`
}

func (s *Service) ListLinkQuestions(linkID string) (*LinkQuestions, error) {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, nil
	}

	matched, err := s.store.ListMatchedQuestions(linkID)
	if err != nil {
		return nil, err
	}
	if matched == nil {
		matched = []LinkQuestion{}
	}

	var createdIDs []string
	if err := json.Unmarshal([]byte(link.CreatedFrom), &createdIDs); err != nil {
		createdIDs = nil
	}
	createdFrom, err := s.store.ListCreatedFromQuestions(createdIDs)
	if err != nil {
		return nil, err
	}
	// Legacy cooccurrence rows stored candidate_id (or empty) — if still
	// empty, fall back to confident traces that cited this point before the
	// link was created (the fuel that made ScanCandidates fire).
	if len(createdFrom) == 0 {
		createdFrom, err = s.store.ListConfidentQuestionsForPointBefore(link.PointID, link.CreatedAt)
		if err != nil {
			return nil, err
		}
	}
	if createdFrom == nil {
		createdFrom = []LinkQuestion{}
	}

	return &LinkQuestions{Matched: matched, CreatedFrom: createdFrom}, nil
}

// Reject implements POST /activation-links/:id/reject (2026-08-13 大幅改写,
// docs/impl/v1/activation.md 步骤 3): valid for ANY status — a link that's
// already verified may still turn out, on human inspection, to rest on
// untrustworthy evidence. Semantics changed from the old candidate→deprecated
// transition: this now means "clear all observed evidence, start over" — the
// link's ObservedConditions are wiped, legacy display fields re-projected
// (empty), and status re-derived (lands on candidate for a current KP, since
// empty conditions can never be verified; stays/becomes deprecated if the KP
// itself is non-current — deriveAndPersistStatus's lifecycle check still
// applies). The link is not deleted and can accumulate fresh evidence later
// via AppendObservedCondition regardless of this rejection. Writes a
// learning_results(action=prune_condition, reason=manual_reject) row — the
// same action Study's automatic convergence pruning will write (阶段 3),
// distinguished by reason/confirmed_by.
func (s *Service) Reject(linkID string) (*ActivationLink, error) {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, fmt.Errorf("activation: link not found: %s", linkID)
	}

	prunedCount := len(link.ObservedConditions)
	if err := s.store.ReplaceObservedConditions(linkID, []ObservedCondition{}); err != nil {
		return nil, err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}

	lr := &LearningResult{
		Action:      ActionPruneCondition,
		ObjectType:  ObjectTypeActivationLink,
		ObjectID:    linkID,
		Reason:      "manual_reject",
		Status:      ResultApplied,
		ConfirmedBy: sql.NullString{String: "manual", Valid: true},
	}
	if err := s.store.InsertLearningResult(lr); err != nil {
		return nil, err
	}

	updated, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	_ = prunedCount
	return updated, nil
}

// ResetBundle implements POST /activation-bundles/:id/reject, the bundle
// counterpart of Service.Reject: "清空该组合链接的全部观测条件与成员置信度，
// 归零重新积累"（docs/impl/v1/activation-bundle.md「成员置信度」），成员名单
// 本身不变。Writes the same learning_results(action=prune_condition,
// reason=manual_reject) row Reject writes, distinguished by ObjectType.
func (s *Service) ResetBundle(bundleID string) (*ActivationBundle, error) {
	b, err := s.store.GetBundleByID(bundleID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("activation: bundle not found: %s", bundleID)
	}
	if err := s.store.ResetBundle(bundleID); err != nil {
		return nil, err
	}
	if s.bundleMatcher != nil {
		s.bundleMatcher.InvalidateCache()
	}

	lr := &LearningResult{
		Action:      ActionPruneCondition,
		ObjectType:  ObjectTypeActivationBundle,
		ObjectID:    bundleID,
		Reason:      "manual_reject",
		Status:      ResultApplied,
		ConfirmedBy: sql.NullString{String: "manual", Valid: true},
	}
	if err := s.store.InsertLearningResult(lr); err != nil {
		return nil, err
	}

	return s.store.GetBundleByID(bundleID)
}

// InvalidateCache implements the unit package's ActivationNotifier interface:
// KP lifecycle changes affect which verified links are eligible to match
// (docs/impl/v1/activation.md 步骤 2 候选加载), so the Matcher cache must
// reload on the next Match call.
func (s *Service) InvalidateCache() error {
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return nil
}

// ListSynonyms implements GET /subject-synonyms (docs/impl/v1/activation.md
// 步骤 3a).
func (s *Service) ListSynonyms(f ListSynonymsFilter) ([]SubjectSynonym, error) {
	return s.store.ListSynonyms(f)
}

func (s *Service) GetSynonym(synonymID string) (*SubjectSynonym, error) {
	return s.store.GetSynonym(synonymID)
}

// ConfirmSynonym implements POST /subject-synonyms/:id/confirm: only valid
// for status=candidate rows. Invalidates the Matcher cache so the new
// mapping is live on the next Match.
func (s *Service) ConfirmSynonym(synonymID string) (*SubjectSynonym, error) {
	syn, err := s.store.GetSynonym(synonymID)
	if err != nil {
		return nil, err
	}
	if syn == nil {
		return nil, fmt.Errorf("activation: synonym not found: %s", synonymID)
	}
	if syn.Status != SynonymStatusCandidate {
		return nil, fmt.Errorf("activation: confirm only valid for candidate synonyms, %s is %s", synonymID, syn.Status)
	}
	if err := s.store.UpdateSynonymStatus(synonymID, SynonymStatusActive); err != nil {
		return nil, err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return s.store.GetSynonym(synonymID)
}

// RejectSynonym implements POST /subject-synonyms/:id/reject: valid for
// status=candidate or status=active rows (2026-08-12 修订: active 起，since
// synonym_auto_promote defaults true now, most gap_mined rows land directly
// on active without ever passing through candidate — reject needs to be able
// to undo an already-live mapping, not just a still-pending one). Rejected
// terms are not automatically revived — a human must resubmit explicitly
// (docs/impl/v1/activation.md 步骤 3a). Invalidates the Matcher cache so a
// revoked active mapping stops matching on the very next Match.
func (s *Service) RejectSynonym(synonymID string) (*SubjectSynonym, error) {
	syn, err := s.store.GetSynonym(synonymID)
	if err != nil {
		return nil, err
	}
	if syn == nil {
		return nil, fmt.Errorf("activation: synonym not found: %s", synonymID)
	}
	if syn.Status != SynonymStatusCandidate && syn.Status != SynonymStatusActive {
		return nil, fmt.Errorf("activation: reject only valid for candidate/active synonyms, %s is %s", synonymID, syn.Status)
	}
	if err := s.store.UpdateSynonymStatus(synonymID, SynonymStatusRejected); err != nil {
		return nil, err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return s.store.GetSynonym(synonymID)
}

// EnrichFromConfidentFullPath appends the current Session quadruple onto every
// non-deprecated ActivationLink whose point was confidently cited on a full
// (slow) path — so the next identical ask can Match without waiting for Study.
func (s *Service) EnrichFromConfidentFullPath(pointIDs []string, subject, intent, audience, constraint, questionTerms string, max int) error {
	if len(pointIDs) == 0 {
		return nil
	}
	add := NormalizeObservedCondition(subject, intent, audience, constraint, intent, constraint, questionTerms, time.Now().UTC())
	for _, pid := range pointIDs {
		link, err := s.store.GetByPointID(pid)
		if err != nil {
			return err
		}
		if link == nil || link.Status == StatusDeprecated {
			continue
		}
		if err := s.AppendObservedCondition(link.LinkID, add, max); err != nil {
			return err
		}
	}
	return nil
}
