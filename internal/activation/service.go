package activation

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jxman78/wiki-brain/internal/session"
)

// WikiNotifier lets Wiki learn a cited KP's ActivationLink just became
// verified, so it can mark dependent pages needs_recompile
// (docs/impl/v1/wiki.md 步骤5 触发(d)) — mirrors unit.WikiNotifier's
// existing cross-module notification shape. SetWikiNotifier no-ops when
// unset.
type WikiNotifier interface {
	NotifyLinkVerified(pointID string) error
}

type Service struct {
	store        *Store
	matcher      *Matcher
	wikiNotifier WikiNotifier
}

func NewService(store *Store, matcher *Matcher) *Service {
	return &Service{store: store, matcher: matcher}
}

func (s *Service) SetWikiNotifier(n WikiNotifier) {
	s.wikiNotifier = n
}

func (s *Service) Store() *Store {
	return s.store
}

// LoadSynonymResolver builds a SynonymResolver warmed with the currently
// active subject synonyms — for callers outside this package (Wiki's
// four-tuple retrieval entry, docs/design/wiki-compilation.md "触发问法取材
// 真实观测，检索匹配复用四元组") that need to call BuildQueryConditionTerms /
// MatchConditionGroups themselves instead of going through Matcher.Match.
func (s *Service) LoadSynonymResolver() (*SynonymResolver, error) {
	synonyms, err := s.store.ListActiveSynonyms()
	if err != nil {
		return nil, fmt.Errorf("activation: load synonym resolver: %w", err)
	}
	r := NewSynonymResolver()
	r.Load(synonyms)
	return r, nil
}

// Match is Retrieval's entry point into the activation layer
// (docs/impl/v1/retrieval.md 步骤 2): a thin passthrough to the Matcher so
// Retrieval only needs one dependency on this package.
func (s *Service) Match(query session.ExpandedQuery, cfg MatchConfig) ([]LinkMatch, error) {
	if s.matcher == nil {
		return nil, nil
	}
	return s.matcher.Match(query, cfg)
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

// TransitionLink is the single entry point for status changes
// (docs/impl/v1/activation.md 状态机 "唯一入口"). It rejects any move not in
// legalTransitions, and on success records a learning_results row and
// invalidates the Matcher's verified-link cache.
func (s *Service) TransitionLink(linkID, to, reason string, eventIDs []string) (*ActivationLink, error) {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, fmt.Errorf("activation: link not found: %s", linkID)
	}

	allowed := legalTransitions[link.Status]
	if !allowed[to] {
		return nil, fmt.Errorf("activation: illegal transition %s -> %s for link %s", link.Status, to, linkID)
	}
	action := transitionAction[link.Status][to]

	eventIDsJSON, err := json.Marshal(eventIDs)
	if err != nil {
		return nil, fmt.Errorf("activation: marshal event ids: %w", err)
	}

	updated, err := s.store.ApplyTransition(linkID, to, action, reason, string(eventIDsJSON))
	if err != nil {
		return nil, err
	}

	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}

	if to == StatusVerified && s.wikiNotifier != nil {
		if err := s.wikiNotifier.NotifyLinkVerified(updated.PointID); err != nil {
			slog.Warn("activation: notify wiki link verified failed", "link_id", linkID, "point_id", updated.PointID, "error", err)
		}
	}

	return updated, nil
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

// Confirm implements POST /activation-links/:id/confirm: only valid for
// candidate links. If Study left a pending_confirm learning_result for this
// link's promotion, its supporting events carry into the new transition
// record and the pending row itself is resolved to applied
// (docs/impl/v1/activation.md 步骤 3 确认/驳回与 Study 的关系).
func (s *Service) Confirm(linkID string) (*ActivationLink, error) {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, fmt.Errorf("activation: link not found: %s", linkID)
	}
	if link.Status != StatusCandidate {
		return nil, fmt.Errorf("activation: confirm only valid for candidate links, link %s is %s", linkID, link.Status)
	}

	eventIDs, pending, err := s.pendingPromoteEventIDs(linkID)
	if err != nil {
		return nil, err
	}

	updated, err := s.TransitionLink(linkID, StatusVerified, "manual_confirm", eventIDs)
	if err != nil {
		return nil, err
	}

	if pending != nil {
		if err := s.store.ResolvePending(pending.ResultID, ResultApplied, "manual"); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// Reject implements POST /activation-links/:id/reject: only valid for
// candidate links.
func (s *Service) Reject(linkID string) (*ActivationLink, error) {
	link, err := s.store.GetByID(linkID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, fmt.Errorf("activation: link not found: %s", linkID)
	}
	if link.Status != StatusCandidate {
		return nil, fmt.Errorf("activation: reject only valid for candidate links, link %s is %s", linkID, link.Status)
	}

	eventIDs, pending, err := s.pendingPromoteEventIDs(linkID)
	if err != nil {
		return nil, err
	}

	updated, err := s.TransitionLink(linkID, StatusDeprecated, "manual_reject", eventIDs)
	if err != nil {
		return nil, err
	}

	if pending != nil {
		if err := s.store.ResolvePending(pending.ResultID, ResultRejected, "manual"); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func (s *Service) pendingPromoteEventIDs(linkID string) ([]string, *LearningResult, error) {
	pending, err := s.store.FindPendingPromote(linkID)
	if err != nil {
		return nil, nil, err
	}
	if pending == nil {
		return nil, nil, nil
	}
	var eventIDs []string
	if err := json.Unmarshal([]byte(pending.EventIDs), &eventIDs); err != nil {
		return nil, nil, fmt.Errorf("activation: unmarshal pending event ids: %w", err)
	}
	return eventIDs, pending, nil
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

// FindSynonymByTerm is Study's dedup check before creating a new gap-mined
// candidate — a term with an existing active/candidate/rejected row is never
// re-proposed (docs/impl/v1/study.md 步骤 2a).
func (s *Service) FindSynonymByTerm(term string) (*SubjectSynonym, error) {
	return s.store.FindSynonymByTermAnyStatus(term)
}

// CreateSynonymCandidate is Study's write path for a subject_synonym_gap pair
// that clears the aggregation threshold (docs/impl/v1/study.md 步骤 2a). No
// cache invalidation needed — candidate rows don't participate in Match
// (only status=active rows do).
func (s *Service) CreateSynonymCandidate(domainID, term, canonical string, createdFrom []string) (*SubjectSynonym, error) {
	return s.store.InsertSynonymCandidate(domainID, term, canonical, createdFrom)
}

// CreateActiveSynonym is the study.synonym_auto_promote=true path: a
// candidate that clears the threshold goes straight to active, no
// pending_confirm (docs/impl/v1/study.md 步骤 2a). Invalidates the Matcher
// cache since active rows do participate in Match.
func (s *Service) CreateActiveSynonym(domainID, term, canonical string, createdFrom []string) (*SubjectSynonym, error) {
	syn, err := s.store.InsertActiveSynonym(domainID, term, canonical, createdFrom)
	if err != nil {
		return nil, err
	}
	if s.matcher != nil {
		s.matcher.InvalidateCache()
	}
	return syn, nil
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

// RejectSynonym implements POST /subject-synonyms/:id/reject: only valid for
// status=candidate rows. Rejected terms are not automatically revived — a
// human must resubmit explicitly (docs/impl/v1/activation.md 步骤 3a).
func (s *Service) RejectSynonym(synonymID string) (*SubjectSynonym, error) {
	syn, err := s.store.GetSynonym(synonymID)
	if err != nil {
		return nil, err
	}
	if syn == nil {
		return nil, fmt.Errorf("activation: synonym not found: %s", synonymID)
	}
	if syn.Status != SynonymStatusCandidate {
		return nil, fmt.Errorf("activation: reject only valid for candidate synonyms, %s is %s", synonymID, syn.Status)
	}
	if err := s.store.UpdateSynonymStatus(synonymID, SynonymStatusRejected); err != nil {
		return nil, err
	}
	return s.store.GetSynonym(synonymID)
}

// FindSynonymGapCandidate checks whether pointID's ActivationLink (if any,
// non-deprecated) has an observed condition group whose intent/audience/
// constraint all match the current query but whose subject does not — a
// "subject-only miss" candidate for the subject synonym dictionary
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Called from Trace alongside EnrichFromConfidentFullPath; read-only, never
// mutates activation_links.
func (s *Service) FindSynonymGapCandidate(pointID, subject, intent, audience, constraint string) (linkID, observedSubject string, ok bool, err error) {
	if s.matcher == nil {
		return "", "", false, nil
	}
	link, err := s.store.GetByPointID(pointID)
	if err != nil {
		return "", "", false, err
	}
	if link == nil || link.Status == StatusDeprecated {
		return "", "", false, nil
	}
	observed, found := s.matcher.SubjectOnlyMiss(link.ObservedConditions, subject, intent, audience, constraint)
	if !found {
		return "", "", false, nil
	}
	return link.LinkID, observed, true, nil
}

// EnrichFromConfidentFullPath appends the current Session quadruple onto every
// non-deprecated ActivationLink whose point was confidently cited on a full
// (slow) path — so the next identical ask can Match without waiting for Study.
func (s *Service) EnrichFromConfidentFullPath(pointIDs []string, subject, intent, audience, constraint, questionTerms string, max int) error {
	if len(pointIDs) == 0 {
		return nil
	}
	add := NormalizeObservedCondition(subject, intent, audience, constraint, questionTerms, time.Now().UTC())
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
