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
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
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

	t := &Trace{
		TraceID:          uuid.New().String(),
		AnswerID:         r.AnswerID,
		Question:         r.Question,
		QuestionHash:     hash,
		QuestionTerms:    groupKey,
		RetrievalQuality: grade.Quality,
		Path:             r.Path,
		DirectPointIDs:   directPointIDs,
	}

	if err := s.store.SaveTrace(t); err != nil {
		slog.Error("trace: save failed", "answer_id", r.AnswerID, "error", err)
		return
	}
	slog.Debug("trace: saved", "trace_id", t.TraceID, "answer_id", r.AnswerID)

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

func (s *Service) SubmitFeedback(traceID string, req FeedbackRequest) error {
	slog.Debug("trace: feedback received", "trace_id", traceID, "type", req.Type)
	if err := s.store.UpdateFeedback(traceID, req.Type, req.Content); err != nil {
		return err
	}

	if req.Type == "negative" || req.Type == "correction" {
		payload, _ := json.Marshal(map[string]string{
			"feedback_content": req.Content,
			"feedback_type":    req.Type,
		})
		if err := s.store.SaveLearningEvent(traceID, "user_correction", string(payload)); err != nil {
			slog.Error("trace: save user_correction event failed", "trace_id", traceID, "error", err)
		}
	}

	return nil
}
