package trace

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

type Service struct {
	store               *Store
	conceptNullRatioMin float64
}

// conceptNullRatioMin is docs/impl/v1/concept-evolution.md's
// study.concept_null_ratio_min — trace only reads this one threshold to
// classify activation_gap events, the rest of concept evolution's config
// lives and is consumed in the study package.
func NewService(store *Store, conceptNullRatioMin float64) *Service {
	return &Service{store: store, conceptNullRatioMin: conceptNullRatioMin}
}

func (s *Service) ProcessTrace(r *answer.AnswerResult) {
	start := time.Now()
	slog.Debug("trace: process start", "answer_id", r.AnswerID, "question", r.Question)

	grade := gradeQuality(r)
	slog.Debug("trace: quality graded",
		"answer_id", r.AnswerID,
		"quality", grade.Quality,
		"direct_point_ids", grade.DirectPointIDs)

	subject := ""
	if r.EvidenceSet != nil {
		subject = r.EvidenceSet.Subject
	}

	normalized := normalize(r.Question)
	// subject is session-resolved (LLM, using conversation history) so it disambiguates
	// literally-identical follow-up questions ("这个怎么算？") that mean different things
	// across sessions; folding it into the hash keeps dedup from merging those.
	hash := questionHash(normalized + "\x1f" + subject)

	// subject groups paraphrases of the same topic together; question_terms (literal
	// tokenization) is only a fallback for the rare case subject wasn't resolved (e.g.
	// a retrieve after the user refused clarification, see session.shouldSkipClarification).
	groupKey := subject
	if groupKey == "" {
		groupKey = questionTerms(normalized)
	}
	slog.Debug("trace: question normalized",
		"answer_id", r.AnswerID,
		"normalized", normalized,
		"hash", hash,
		"subject", subject,
		"group_key", groupKey)

	directPointIDs := grade.DirectPointIDs
	if directPointIDs == nil {
		directPointIDs = []string{}
	}

	pathType := retrieval.PathTypeFull
	var activationLinkIDs []string
	var intent, audience, constraintText string
	if r.EvidenceSet != nil {
		if r.EvidenceSet.PathType != "" {
			pathType = r.EvidenceSet.PathType
		}
		for _, hit := range r.EvidenceSet.ActivationHits {
			activationLinkIDs = append(activationLinkIDs, hit.LinkID)
		}
		intent = r.EvidenceSet.Intent
		audience = r.EvidenceSet.Audience
		constraintText = r.EvidenceSet.Constraint
	}

	t := &Trace{
		TraceID:           uuid.New().String(),
		AnswerID:          r.AnswerID,
		Question:          r.Question,
		QuestionHash:      hash,
		QuestionTerms:     groupKey,
		RetrievalQuality:  grade.Quality,
		Path:              r.Path,
		PathType:          pathType,
		ActivationLinkIDs: activationLinkIDs,
		Subject:           subject,
		Intent:            intent,
		Audience:          audience,
		ConstraintText:    constraintText,
		DirectPointIDs:    directPointIDs,
		KPNCitedCount:     grade.KPNCitedCount,
		CitedCount:        grade.CitedCount,
	}

	if err := s.store.SaveTrace(t); err != nil {
		slog.Error("trace: save failed", "answer_id", r.AnswerID, "error", err)
		return
	}
	slog.Debug("trace: saved", "trace_id", t.TraceID, "answer_id", r.AnswerID)

	s.generateActivationEvents(t, r, grade)
	s.updateCooccurrence(t, r)
	s.generateLearningEvents(t)

	slog.Debug("trace: process complete",
		"trace_id", t.TraceID,
		"quality", t.RetrievalQuality,
		"duration_ms", time.Since(start).Milliseconds())
}

func (s *Service) updateCooccurrence(t *Trace, r *answer.AnswerResult) {
	var pointIDs []string

	switch t.RetrievalQuality {
	case QualityConfident:
		pointIDs = t.DirectPointIDs
	case QualityPartial:
		pointIDs = supportingCitedPointIDs(r.EvidenceSet, r.Citations)
	case QualityGap:
		slog.Debug("trace: cooccurrence skipped (gap)", "trace_id", t.TraceID)
		return
	}

	if len(pointIDs) == 0 {
		slog.Debug("trace: cooccurrence skipped (no point_ids)", "trace_id", t.TraceID, "quality", t.RetrievalQuality)
		return
	}

	slog.Debug("trace: cooccurrence update",
		"trace_id", t.TraceID,
		"quality", t.RetrievalQuality,
		"point_ids", pointIDs,
		"question_hash", t.QuestionHash)
	if err := s.store.UpdateCooccurrence(t.QuestionHash, t.QuestionTerms, pointIDs, t.RetrievalQuality); err != nil {
		slog.Error("trace: cooccurrence update failed", "trace_id", t.TraceID, "error", err)
	}
}

func supportingCitedPointIDs(es *retrieval.EvidenceSet, citations []string) []string {
	if es == nil {
		return nil
	}

	citedSet := make(map[string]bool, len(citations))
	for _, fid := range citations {
		citedSet[fid] = true
	}

	supportFactToPoint := make(map[string]string, len(es.Supporting))
	for _, e := range es.Supporting {
		supportFactToPoint[e.FactID] = e.PointID
	}

	seen := make(map[string]bool)
	var result []string
	for _, fid := range citations {
		pid, ok := supportFactToPoint[fid]
		if ok && pid != "" && !seen[pid] {
			seen[pid] = true
			result = append(result, pid)
		}
	}
	return result
}

// generateActivationEvents grades each activation-layer hit carried on the
// AnswerResult into activation_success / activation_failure, or — when the
// activation layer found nothing but the full path still landed a confident
// answer — a single activation_gap event (docs/impl/v1/trace.md 步骤 3).
// Runs after SaveTrace, before cooccurrence update; purely program logic,
// no LLM calls.
func (s *Service) generateActivationEvents(t *Trace, r *answer.AnswerResult, grade gradeResult) {
	if t.PathType == retrieval.PathTypeWiki {
		return
	}

	var hits []retrieval.ActivationHit
	if r.EvidenceSet != nil {
		hits = r.EvidenceSet.ActivationHits
	}

	if len(hits) == 0 {
		if t.PathType == retrieval.PathTypeFull && t.RetrievalQuality == QualityConfident {
			directPointIDs := nonNilStrings(t.DirectPointIDs)

			nullRatio, err := s.store.ConceptNullRatio(directPointIDs)
			if err != nil {
				slog.Error("trace: concept null ratio lookup failed", "trace_id", t.TraceID, "error", err)
			}
			gapLevel := "link_gap"
			if nullRatio >= s.conceptNullRatioMin {
				gapLevel = "concept_gap"
			}

			payload, _ := json.Marshal(map[string]interface{}{
				"question_terms":     t.QuestionTerms,
				"direct_point_ids":   directPointIDs,
				"gap_level":          gapLevel,
				"null_concept_ratio": nullRatio,
			})
			slog.Debug("trace: generating activation_gap event", "trace_id", t.TraceID, "gap_level", gapLevel, "null_concept_ratio", nullRatio)
			if err := s.store.SaveLearningEvent(t.TraceID, "activation_gap", string(payload)); err != nil {
				slog.Error("trace: save activation_gap event failed", "trace_id", t.TraceID, "error", err)
			}
		}
		return
	}

	directSet := make(map[string]bool, len(grade.DirectPointIDs))
	for _, pid := range grade.DirectPointIDs {
		directSet[pid] = true
	}
	factsByPoint := citedFactIDsByPoint(r.EvidenceSet, r.Citations)

	for _, hit := range hits {
		if directSet[hit.PointID] {
			payload, _ := json.Marshal(map[string]interface{}{
				"link_id":        hit.LinkID,
				"point_id":       hit.PointID,
				"question_terms": t.QuestionTerms,
				"match_score":    hit.MatchScore,
				"cited_fact_ids": nonNilStrings(factsByPoint[hit.PointID]),
			})
			slog.Debug("trace: generating activation_success event", "trace_id", t.TraceID, "link_id", hit.LinkID)
			if err := s.store.SaveLearningEvent(t.TraceID, "activation_success", string(payload)); err != nil {
				slog.Error("trace: save activation_success event failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
			}
			continue
		}

		reason := "not_cited"
		switch {
		case r.Path == "error":
			reason = "answer_error"
		case t.RetrievalQuality == QualityGap:
			reason = "answer_gap"
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"link_id":        hit.LinkID,
			"point_id":       hit.PointID,
			"question_terms": t.QuestionTerms,
			"match_score":    hit.MatchScore,
			"reason":         reason,
		})
		slog.Debug("trace: generating activation_failure event", "trace_id", t.TraceID, "link_id", hit.LinkID, "reason", reason)
		if err := s.store.SaveLearningEvent(t.TraceID, "activation_failure", string(payload)); err != nil {
			slog.Error("trace: save activation_failure event failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
		}
	}
}

// citedFactIDsByPoint maps each direct-evidence point_id to the fact_ids
// Answer actually cited for it — the activation_success payload's
// cited_fact_ids (docs/impl/v1/trace.md payload 结构).
func citedFactIDsByPoint(es *retrieval.EvidenceSet, citations []string) map[string][]string {
	result := make(map[string][]string)
	if es == nil {
		return result
	}
	citedSet := make(map[string]bool, len(citations))
	for _, fid := range citations {
		citedSet[fid] = true
	}
	for _, e := range es.DirectEvidence {
		if citedSet[e.FactID] {
			result[e.PointID] = append(result[e.PointID], e.FactID)
		}
	}
	return result
}

func (s *Service) generateLearningEvents(t *Trace) {
	if t.RetrievalQuality == QualityGap {
		slog.Debug("trace: generating knowledge_gap event", "trace_id", t.TraceID, "question", t.Question)
		payload, _ := json.Marshal(map[string]string{"question": t.Question})
		if err := s.store.SaveLearningEvent(t.TraceID, "knowledge_gap", string(payload)); err != nil {
			slog.Error("trace: save knowledge_gap event failed", "trace_id", t.TraceID, "error", err)
		}
	} else {
		slog.Debug("trace: no learning event needed", "trace_id", t.TraceID, "quality", t.RetrievalQuality)
	}
}

// SubmitFeedback records user feedback on t and, for negative/correction
// feedback on a fast-path trace, tags the resulting user_correction event
// with the activation links that produced the answer — so Study can direct
// the correction's weakening signal at those specific links instead of only
// the global cooccurrence stats (docs/impl/v1/trace.md 步骤 4).
func (s *Service) SubmitFeedback(t *Trace, req FeedbackRequest) error {
	slog.Debug("trace: feedback received", "trace_id", t.TraceID, "type", req.Type)
	if err := s.store.UpdateFeedback(t.TraceID, req.Type, req.Content); err != nil {
		return err
	}

	if req.Type == "negative" || req.Type == "correction" {
		fields := map[string]interface{}{
			"feedback_content": req.Content,
			"feedback_type":    req.Type,
		}
		if len(t.ActivationLinkIDs) > 0 {
			fields["link_ids"] = t.ActivationLinkIDs
		}
		payload, _ := json.Marshal(fields)
		if err := s.store.SaveLearningEvent(t.TraceID, "user_correction", string(payload)); err != nil {
			slog.Error("trace: save user_correction event failed", "trace_id", t.TraceID, "error", err)
		}
	}

	return nil
}
