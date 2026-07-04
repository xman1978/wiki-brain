package activation

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

type Service struct {
	store   *Store
	matcher *Matcher
}

func NewService(store *Store, matcher *Matcher) *Service {
	return &Service{store: store, matcher: matcher}
}

func (s *Service) Store() *Store {
	return s.store
}

// CreateLink is idempotent on (question_terms, point_id): a second call with
// the same pair returns the existing link unchanged. If that existing link
// was deprecated, creation is refused rather than reviving it — a deprecated
// condition needs a human/Study decision informed by fresh accumulated
// signal, not an automatic recreate (docs/impl/v1/activation.md 步骤 1).
func (s *Service) CreateLink(questionTerms string, cond LinkCondition, pointID string, createdFrom []string) (*ActivationLink, error) {
	existing, err := s.store.GetByQuestionAndPoint(questionTerms, pointID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Status == StatusDeprecated {
			slog.Info("activation: refusing to recreate deprecated link",
				"link_id", existing.LinkID, "question_terms", questionTerms, "point_id", pointID)
			return nil, fmt.Errorf("activation: link %s for (question_terms=%q, point_id=%s) is deprecated; refusing to recreate",
				existing.LinkID, questionTerms, pointID)
		}
		return existing, nil
	}

	createdFromJSON, err := json.Marshal(createdFrom)
	if err != nil {
		return nil, fmt.Errorf("activation: marshal created_from: %w", err)
	}

	link := &ActivationLink{
		QuestionTerms:   questionTerms,
		SubjectTerms:    cond.SubjectTerms,
		IntentTerms:     cond.IntentTerms,
		Audience:        cond.Audience,
		ConstraintTerms: cond.ConstraintTerms,
		PointID:         pointID,
		Status:          StatusCandidate,
		CreatedFrom:     string(createdFromJSON),
	}
	if err := s.store.InsertLink(link); err != nil {
		return nil, err
	}
	return s.store.GetByID(link.LinkID)
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
