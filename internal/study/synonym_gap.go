package study

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jxman78/wiki-brain/internal/activation"
)

// synonymGapPayload mirrors the subject_synonym_gap event payload written by
// Trace (docs/impl/v1/trace.md 步骤 3).
type synonymGapPayload struct {
	PointID         string `json:"point_id"`
	LinkID          string `json:"link_id"`
	QuerySubject    string `json:"query_subject"`
	ObservedSubject string `json:"observed_subject"`
	QuestionTerms   string `json:"question_terms"`
}

type synonymGapAgg struct {
	hitCount       int
	valueCounts    map[string]int
	distinctHashes map[string]bool
	eventIDs       []string
}

const synonymPairSep = "\x1f"

func synonymPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + synonymPairSep + b
}

func splitSynonymPairKey(key string) (a, b string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '\x1f' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// aggregateAndCreateSynonymCandidates implements docs/impl/v1/study.md
// 步骤 2a: group unprocessed subject_synonym_gap events by the unordered
// (query_subject, observed_subject) pair, and for pairs clearing the
// threshold, create (or auto-promote) a subject_synonyms candidate — unless
// a row for that term already exists in any status
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Log-and-continue on a single group's failure, matching this Run cycle's
// "单步异常记录 error 日志，不中断本轮后续步骤" convention.
func (s *Service) aggregateAndCreateSynonymCandidates(actions *LearningActionsSummary) error {
	events, err := s.store.FetchUnprocessedSynonymGapEvents()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}

	groups := make(map[string]*synonymGapAgg)
	for _, e := range events {
		var p synonymGapPayload
		malformed := false
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			malformed = true
		} else if p.QuerySubject == "" || p.ObservedSubject == "" || p.QuerySubject == p.ObservedSubject {
			malformed = true
		}
		if malformed {
			slog.Warn("study: subject_synonym_gap payload malformed or degenerate, skipping", "event_id", e.EventID)
			if err := s.store.MarkEventProcessed(e.EventID); err != nil {
				return err
			}
			continue
		}

		key := synonymPairKey(p.QuerySubject, p.ObservedSubject)
		agg, ok := groups[key]
		if !ok {
			agg = &synonymGapAgg{valueCounts: make(map[string]int), distinctHashes: make(map[string]bool)}
			groups[key] = agg
		}
		agg.hitCount++
		agg.valueCounts[p.ObservedSubject]++
		if e.QuestionHash != "" {
			agg.distinctHashes[e.QuestionHash] = true
		}
		agg.eventIDs = append(agg.eventIDs, e.EventID)

		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			return err
		}
	}

	for key, agg := range groups {
		distinctN := len(agg.distinctHashes)
		if agg.hitCount < s.cfg.SynonymGapMin || distinctN < s.cfg.SynonymGapDistinctMin {
			continue
		}

		a, b := splitSynonymPairKey(key)
		canonical, term := pickSynonymDirection(a, b, agg.valueCounts)

		existing, err := s.activationSvc.FindSynonymByTerm(term)
		if err != nil {
			slog.Error("study: find synonym by term failed", "term", term, "error", err)
			continue
		}
		if existing != nil {
			// active/candidate/rejected already covers this term — never
			// re-propose (docs/impl/v1/study.md 步骤 2a).
			continue
		}

		reason := fmt.Sprintf("subject_synonym_gap 聚合：hit_count=%d, distinct_questions=%d", agg.hitCount, distinctN)

		if s.cfg.SynonymAutoPromote {
			syn, err := s.activationSvc.CreateActiveSynonym("", term, canonical, agg.eventIDs)
			if err != nil {
				slog.Error("study: create active synonym failed", "term", term, "canonical", canonical, "error", err)
				continue
			}
			lr := &activation.LearningResult{
				Action:      activation.ActionSynonymCandidate,
				ObjectType:  activation.ObjectTypeSubjectSynonym,
				ObjectID:    syn.SynonymID,
				Reason:      reason,
				EventIDs:    marshalIDs(agg.eventIDs),
				Status:      activation.ResultApplied,
				ConfirmedBy: sql.NullString{String: "auto", Valid: true},
			}
			if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
				slog.Error("study: insert synonym_candidate (auto) learning result failed", "synonym_id", syn.SynonymID, "error", err)
			}
			actions.SynonymCandidatesCreated++
			continue
		}

		syn, err := s.activationSvc.CreateSynonymCandidate("", term, canonical, agg.eventIDs)
		if err != nil {
			slog.Error("study: create synonym candidate failed", "term", term, "canonical", canonical, "error", err)
			continue
		}
		lr := &activation.LearningResult{
			Action:     activation.ActionSynonymCandidate,
			ObjectType: activation.ObjectTypeSubjectSynonym,
			ObjectID:   syn.SynonymID,
			Reason:     reason,
			EventIDs:   marshalIDs(agg.eventIDs),
			Status:     activation.ResultPendingConfirm,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert synonym_candidate learning result failed", "synonym_id", syn.SynonymID, "error", err)
		}
		actions.SynonymCandidatesCreated++
	}

	return nil
}

// pickSynonymDirection implements the deterministic canonical/term rule
// (docs/impl/v1/study.md 步骤 2a): the value backed by more history as an
// already-established ActivationLink observed_subject wins as canonical —
// the newly-asked wording becomes the term that resolves to it. Ties break
// on the lexicographically smaller string so repeated runs never flip
// direction.
func pickSynonymDirection(a, b string, observedCounts map[string]int) (canonical, term string) {
	ca, cb := observedCounts[a], observedCounts[b]
	switch {
	case ca > cb:
		return a, b
	case cb > ca:
		return b, a
	case a < b:
		return a, b
	default:
		return b, a
	}
}
