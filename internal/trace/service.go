package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

type Service struct {
	store                 *Store
	conceptNullRatioMin   float64
	enricher              ObservedConditionEnricher
	observedConditionsMax int
	correctionWeight      int
	synthesisWriter       SynthesisOutcomeWriter
	llmClient             llm.LLMClient
}

// ObservedConditionEnricher is implemented by activation.Service — optional so
// trace tests without activation still run.
type ObservedConditionEnricher interface {
	EnrichFromConfidentFullPath(pointIDs []string, subject, intent, audience, constraint, questionTerms string, max int) error
	// FindSynonymGapCandidate is the read-only diagnostic behind the
	// subject_synonym_gap event (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md):
	// it reports whether pointID's existing link has an observed group whose
	// intent/audience/constraint match but whose subject doesn't.
	FindSynonymGapCandidate(pointID, subject, intent, audience, constraint string) (linkID, observedSubject string, ok bool, err error)
	// RecordOutcome/RecordAuditOutcome (2026-08-13, docs/impl/v1/trace.md 步骤
	// 3/3b/4) update the matched observed condition's success_count/
	// failure_count (and, for the audit variant, audited_success_count/
	// audited_failure_count) — the continuous-confidence replacement for the
	// old discrete promote/weaken state machine (docs/design/activation-convergence.md).
	RecordOutcome(linkID, subject, intent, audience, constraint string, success bool, questionTerms string) error
	RecordAuditOutcome(linkID, subject, intent, audience, constraint string, agree bool) error
	// RecordBundleOutcome/RecordMemberOutcome (2026-08-20, docs/impl/v1/
	// activation-bundle.md「验证」阶段 2 接线) mirror RecordOutcome for
	// ActivationBundle's two axes — the Bundle-level trigger condition and
	// each individual member point resolved into this round's hit.
	RecordBundleOutcome(bundleID, subject, intent, audience, constraint string, success bool) error
	RecordMemberOutcome(bundleID, pointID string, success bool) error
}

// conceptNullRatioMin is docs/impl/v1/concept-evolution.md's
// study.entry_null_ratio_min — trace only reads this one threshold to
// classify activation_gap events, the rest of concept evolution's config
// lives and is consumed in the study package.
func NewService(store *Store, conceptNullRatioMin float64) *Service {
	return &Service{store: store, conceptNullRatioMin: conceptNullRatioMin}
}

// SetObservedConditionEnricher wires slow-path Append onto existing links.
func (s *Service) SetObservedConditionEnricher(e ObservedConditionEnricher, max int) {
	s.enricher = e
	s.observedConditionsMax = max
}

// SetCorrectionWeight configures how many times a single negative/correction
// feedback calls RecordOutcome(success=false) per linked ActivationLink
// (docs/impl/v1/trace.md 步骤 4) — the direct replacement for Study's removed
// window-based weakening, sourced from StudyConfig.CorrectionWeight.
func (s *Service) SetCorrectionWeight(n int) {
	s.correctionWeight = n
}

// SetLLMClient wires the LLM call resolvePointBinding uses to re-derive
// which KP a multi-KP unit's direct evidence actually corresponds to
// (2026-08-16 design decision — see chat: retrieval-time point_id tagging on
// a whole-unit candidate is a guess made before the answer exists; this
// async, post-answer call has the question, the generated answer, and every
// sibling KP's own content to judge from, so it corrects the tag rather than
// inheriting it). A nil client (activation-less test setups) leaves
// resolvePointBinding a no-op.
func (s *Service) SetLLMClient(c llm.LLMClient) {
	s.llmClient = c
}

func (s *Service) ProcessTrace(r *answer.AnswerResult) {
	start := time.Now()
	slog.Debug("trace: process start", "answer_id", r.AnswerID, "question", r.Question)

	grade := gradeQuality(r)
	s.resolveDirectEvidence(&grade, r)
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
		OutlineCitedCount: grade.OutlineCitedCount,
		CitedRankSum:      grade.CitedRankSum,
	}

	if err := s.store.SaveTrace(t); err != nil {
		slog.Error("trace: save failed", "answer_id", r.AnswerID, "error", err)
		return
	}
	slog.Debug("trace: saved", "trace_id", t.TraceID, "answer_id", r.AnswerID)

	s.generateActivationEvents(t, r, grade)
	s.generateBundleActivationEvents(t, r, grade)
	s.updateCooccurrence(t, r)
	s.enrichObservedConditions(t)
	s.generateLearningEvents(t, r)
	s.generateTopNCalibrationEvent(t, r, grade)

	slog.Debug("trace: process complete",
		"trace_id", t.TraceID,
		"quality", t.RetrievalQuality,
		"duration_ms", time.Since(start).Milliseconds())
}

// resolveDirectEvidence is the single post-answer correction pass over a
// confident grade's DirectPointIDs, replacing what used to be two separate
// steps (2026-08-16 design — see chat):
//
//   - point-binding correction: a unit-level retrieval candidate can only
//     carry one nominal point_id, chosen as a guess before the answer
//     existed, even when its text spans several sibling KPs;
//   - constraint-consistency gating: docs/impl/v1/trace.md's 约束一致性判定
//     used to compare the question's constraint against a unit-level
//     semantic summary (source_theme/content_theme/object/scope), which is
//     too coarse to reflect a specific sibling KP's own numeric/detail
//     content and produced false-positive drops (e.g. constraint "逾期三个月"
//     vs a KP whose content says "90天" — same fact, different surface form).
//
// For a unit with >1 current KP (ambiguous — AmbiguousUnitPoints), both
// jobs are folded into one resolve_kp_binding.md call: the LLM sees the
// question, the generated answer, and every sibling KP's own content, so it
// judges both "which KP(s) does this answer actually correspond to" and
// "does the question's constraint actually hold for that KP's content"
// (semantic equivalence tolerated) in a single pass — no separate
// unit-summary-based check runs for these points.
//
// For a unit with 0 or 1 current KP (unambiguous — never reaches the LLM,
// per "仅多 KP unit 触发" 2026-08-16 决策), constraint consistency still
// needs checking, but there is no candidate to pick between — it falls back
// to the same deterministic term-overlap rule as before, just against that
// one point's own content instead of the unit-level summary.
//
// LLM/parse/lookup failures are non-fatal: the affected point_id(s) are
// kept as originally tagged and the rest of trace_write proceeds unaffected.
func (s *Service) resolveDirectEvidence(grade *gradeResult, r *answer.AnswerResult) {
	es := r.EvidenceSet
	if grade.Quality != QualityConfident || es == nil || es.PathType == retrieval.PathTypeWiki {
		return
	}
	directPointIDs := grade.DirectPointIDs
	if len(directPointIDs) == 0 {
		return
	}

	pointToUnit := make(map[string]string, len(es.DirectEvidence))
	for _, ev := range es.DirectEvidence {
		if ev.PointID != "" {
			pointToUnit[ev.PointID] = ev.UnitID
		}
	}

	unitSeen := make(map[string]bool)
	var unitIDs []string
	for _, pid := range directPointIDs {
		if uid, ok := pointToUnit[pid]; ok && uid != "" && !unitSeen[uid] {
			unitSeen[uid] = true
			unitIDs = append(unitIDs, uid)
		}
	}

	var ambiguous map[string][]PointSummary
	if s.llmClient != nil && len(unitIDs) > 0 {
		var err error
		ambiguous, err = s.store.AmbiguousUnitPoints(unitIDs)
		if err != nil {
			slog.Warn("trace: resolve direct evidence: load ambiguous unit points failed", "answer_id", r.AnswerID, "error", err)
		}
	}

	// Split into points whose unit the LLM will vet (ambiguous) vs points
	// that fall back to the deterministic own-content constraint check.
	var ambiguousUnitOrder []string
	ambiguousUnitSeen := make(map[string]bool)
	simplePoints := make([]string, 0, len(directPointIDs))
	for _, pid := range directPointIDs {
		uid := pointToUnit[pid]
		if _, ok := ambiguous[uid]; ok {
			if !ambiguousUnitSeen[uid] {
				ambiguousUnitSeen[uid] = true
				ambiguousUnitOrder = append(ambiguousUnitOrder, uid)
			}
			continue
		}
		simplePoints = append(simplePoints, pid)
	}

	resolvedByUnit := make(map[string][]string, len(ambiguousUnitOrder))
	if len(ambiguousUnitOrder) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, uid := range ambiguousUnitOrder {
			resolved, ok := s.resolveUnitBinding(ctx, r.Question, r.Content, ambiguous[uid])
			if !ok {
				// Fall back to this unit's originally-tagged point_id(s),
				// still subject to the deterministic own-content check below.
				for _, pid := range directPointIDs {
					if pointToUnit[pid] == uid {
						simplePoints = append(simplePoints, pid)
					}
				}
				continue
			}
			resolvedByUnit[uid] = resolved
		}
	}

	kept := s.filterByConstraintOwnContent(simplePoints, es.Constraint, r.AnswerID)

	out := make([]string, 0, len(directPointIDs))
	seen := make(map[string]bool, len(directPointIDs))
	addUnique := func(pid string) {
		if pid != "" && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	for _, pid := range kept {
		addUnique(pid)
	}
	for _, uid := range ambiguousUnitOrder {
		for _, pid := range resolvedByUnit[uid] {
			addUnique(pid)
		}
	}

	if len(out) == 0 {
		grade.Quality = QualityPartial
	}
	grade.DirectPointIDs = out
}

// filterByConstraintOwnContent applies 约束一致性判定 (constraint.go) to a
// set of unambiguous points, using each point's own content as the evidence
// side of the term-overlap check (replacing the old unit-level semantic
// summary — see resolveDirectEvidence doc comment). A content lookup
// failure skips the gate for the affected points rather than blocking the
// trace (matches the old gate's fail-open behavior).
func (s *Service) filterByConstraintOwnContent(pointIDs []string, constraint, answerID string) []string {
	if constraint == "" || len(pointIDs) == 0 {
		return pointIDs
	}
	items := splitConstraintItems(constraint)
	if len(items) == 0 {
		return pointIDs
	}

	contents, err := s.store.PointContents(pointIDs)
	if err != nil {
		slog.Error("trace: constraint gate content lookup failed", "answer_id", answerID, "error", err)
		return pointIDs
	}

	var kept, dropped []string
	for _, pid := range pointIDs {
		if pointConflictsWithConstraint(items, text.TermSet(contents[pid])) {
			dropped = append(dropped, pid)
			continue
		}
		kept = append(kept, pid)
	}
	if len(dropped) > 0 {
		slog.Info("trace: constraint mismatch dropped direct citations",
			"answer_id", answerID, "constraint", constraint, "dropped_point_ids", dropped)
	}
	return kept
}

// resolveUnitBinding runs one resolve_kp_binding.md call for a single
// ambiguous unit — judging both which KP(s) the answer corresponds to and
// whether the question's constraint actually holds for their content. ok=false
// on any LLM/parse failure or an empty candidate set — the caller falls back
// to that unit's originally-tagged point_id(s) through the deterministic
// own-content constraint check.
func (s *Service) resolveUnitBinding(ctx context.Context, question, answerContent string, candidates []PointSummary) (resolved []string, ok bool) {
	if len(candidates) == 0 {
		return nil, false
	}

	valid := make(map[string]bool, len(candidates))
	var candidatesText strings.Builder
	for _, c := range candidates {
		valid[c.PointID] = true
		fmt.Fprintf(&candidatesText, "【%s】\n%s\n\n", c.PointID, c.Content)
	}

	raw, err := s.llmClient.CompleteJSON(ctx, "resolve_kp_binding.md", map[string]string{
		"question":   question,
		"answer":     answerContent,
		"candidates": candidatesText.String(),
	}, "classification")
	if err != nil {
		slog.Warn("trace: resolve point binding: llm call failed", "error", err)
		return nil, false
	}

	var result struct {
		PointIDs []string `json:"point_ids"`
		Reason   string   `json:"reason"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		slog.Warn("trace: resolve point binding: parse response failed", "error", err)
		return nil, false
	}

	out := make([]string, 0, len(result.PointIDs))
	for _, pid := range result.PointIDs {
		if valid[pid] {
			out = append(out, pid)
		} else {
			slog.Warn("trace: resolve point binding: llm returned out-of-candidate point_id, dropped", "point_id", pid)
		}
	}
	slog.Debug("trace: resolve point binding done", "resolved", out, "reason", result.Reason)
	return out, true
}

// enrichObservedConditions appends this turn's Session quadruple onto links
// for confidently cited points on the full path (slow path / fast fallback),
// and — before appending — mines subject_synonym_gap candidates from links
// that would have matched if not for the subject wording
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
func (s *Service) enrichObservedConditions(t *Trace) {
	if s.enricher == nil {
		return
	}
	if t.RetrievalQuality != QualityConfident || t.PathType != retrieval.PathTypeFull {
		return
	}
	if len(t.DirectPointIDs) == 0 {
		return
	}

	s.detectSubjectSynonymGaps(t)

	if err := s.enricher.EnrichFromConfidentFullPath(
		t.DirectPointIDs, t.Subject, t.Intent, t.Audience, t.ConstraintText, t.QuestionTerms,
		s.observedConditionsMax,
	); err != nil {
		slog.Error("trace: observed condition enrich failed", "trace_id", t.TraceID, "error", err)
	}
}

// detectSubjectSynonymGaps runs under the same eligibility gate as
// enrichment (checked by the caller: full path, confident, direct points
// non-empty) but is read-only. For each direct point with an existing
// non-deprecated ActivationLink whose intent/audience/constraint match this
// turn's query but whose subject doesn't (even after currently-registered
// synonym canonicalization), it records one subject_synonym_gap learning
// event — Study's aggregation input for confirming new synonym candidates
// (docs/impl/v1/study.md 步骤 2a). Runs before EnrichFromConfidentFullPath so
// the diagnostic reflects the link's state prior to this turn's own
// enrichment (docs/impl/v1/trace.md 步骤 3).
func (s *Service) detectSubjectSynonymGaps(t *Trace) {
	for _, pid := range t.DirectPointIDs {
		linkID, observedSubject, ok, err := s.enricher.FindSynonymGapCandidate(pid, t.Subject, t.Intent, t.Audience, t.ConstraintText)
		if err != nil {
			slog.Error("trace: subject synonym gap detection failed", "trace_id", t.TraceID, "point_id", pid, "error", err)
			continue
		}
		if !ok {
			continue
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"point_id":         pid,
			"link_id":          linkID,
			"query_subject":    normalize(t.Subject),
			"observed_subject": observedSubject,
			"question_terms":   t.QuestionTerms,
		})
		slog.Debug("trace: generating subject_synonym_gap event", "trace_id", t.TraceID, "point_id", pid, "link_id", linkID)
		if _, err := s.store.SaveLearningEvent(t.TraceID, "subject_synonym_gap", string(payload)); err != nil {
			slog.Error("trace: save subject_synonym_gap event failed", "trace_id", t.TraceID, "error", err)
		}
	}
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

	// subject/intent 均未解析出（如裸调 POST /answer 跳过了 /session/turn，或
	// 解析本身失败）时，question_terms 会退化成对问题原文的字面分词——这只是
	// 兜底容错，不是一个可信的"同一类问题"身份，用它继续聚合会把彼此无关的
	// 问题在 confident_count 上悄悄混在一起（2026-08-24 实测案例：4 条 KP 混杂
	// 的空四元组 ActivationBundle）。四元组两个字段都缺失时直接放弃这次聚合，
	// 宁可这条 trace 的信号丢失，也不让它污染共现累积。
	if t.Subject == "" && t.Intent == "" {
		slog.Debug("trace: cooccurrence skipped (empty subject and intent)", "trace_id", t.TraceID)
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

			nullRatio, err := s.store.EntryNullRatio(directPointIDs)
			if err != nil {
				slog.Error("trace: concept null ratio lookup failed", "trace_id", t.TraceID, "error", err)
			}
			gapLevel := "link_gap"
			if nullRatio >= s.conceptNullRatioMin {
				gapLevel = "entry_gap"
			}

			payload, _ := json.Marshal(map[string]interface{}{
				"question_terms":   t.QuestionTerms,
				"direct_point_ids": directPointIDs,
				"gap_level":        gapLevel,
				"null_entry_ratio": nullRatio,
			})
			slog.Debug("trace: generating activation_gap event", "trace_id", t.TraceID, "gap_level", gapLevel, "null_entry_ratio", nullRatio)
			if _, err := s.store.SaveLearningEvent(t.TraceID, "activation_gap", string(payload)); err != nil {
				slog.Error("trace: save activation_gap event failed", "trace_id", t.TraceID, "error", err)
			}
		}
		return
	}

	directSet := make(map[string]bool, len(grade.DirectPointIDs))
	for _, pid := range grade.DirectPointIDs {
		directSet[pid] = true
	}
	var supportingEvidence []retrieval.Evidence
	if r.EvidenceSet != nil {
		supportingEvidence = r.EvidenceSet.Supporting
	}
	factsByPoint := citedFactIDsByEvidence(directEvidenceOf(r.EvidenceSet), r.Citations)
	supportingFactsByPoint := citedFactIDsByEvidence(supportingEvidence, r.Citations)

	for _, hit := range hits {
		if directSet[hit.PointID] {
			payload, _ := json.Marshal(map[string]interface{}{
				"link_id":        hit.LinkID,
				"point_id":       hit.PointID,
				"question_terms": t.QuestionTerms,
				"match_score":    hit.MatchScore,
				"cited_fact_ids": nonNilStrings(factsByPoint[hit.PointID]),
				"role":           "direct",
			})
			slog.Debug("trace: generating activation_success event", "trace_id", t.TraceID, "link_id", hit.LinkID, "role", "direct")
			if _, err := s.store.SaveLearningEvent(t.TraceID, "activation_success", string(payload)); err != nil {
				slog.Error("trace: save activation_success event failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
			}
			s.recordHitOutcome(t, hit, true)
			continue
		}

		if cited := supportingFactsByPoint[hit.PointID]; len(cited) > 0 {
			payload, _ := json.Marshal(map[string]interface{}{
				"link_id":        hit.LinkID,
				"point_id":       hit.PointID,
				"question_terms": t.QuestionTerms,
				"match_score":    hit.MatchScore,
				"cited_fact_ids": nonNilStrings(cited),
				"role":           "supporting",
			})
			slog.Debug("trace: generating activation_success event", "trace_id", t.TraceID, "link_id", hit.LinkID, "role", "supporting")
			if _, err := s.store.SaveLearningEvent(t.TraceID, "activation_success", string(payload)); err != nil {
				slog.Error("trace: save activation_success event failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
			}
			s.recordHitOutcome(t, hit, true)
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
		if _, err := s.store.SaveLearningEvent(t.TraceID, "activation_failure", string(payload)); err != nil {
			slog.Error("trace: save activation_failure event failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
		}
		s.recordHitOutcome(t, hit, false)
	}
}

// generateBundleActivationEvents mirrors generateActivationEvents for
// ActivationBundle hits (docs/impl/v1/activation-bundle.md「验证」, 2026-08-20
// 阶段 2 接线) — reuses the same activation_success/activation_failure event
// types (no new event_type, per that document's decision) with bundle_id/
// member_point_ids in the payload instead of link_id, so审计浏览不需要区分
// 两套事件表. A Bundle hit's success is judged at the member level (was this
// specific member point actually cited direct or supporting), same signal
// Link uses per-point — the Bundle's own trigger-axis outcome is "did at
// least one of its resolved members end up cited", not an independent
// judgement, since the Bundle's whole reason for being consulted was to
// resolve exactly those members for this question. No activation_gap
// fallback here — that's Link's "activation layer found nothing" diagnostic,
// orthogonal to whether a Bundle happened to also match.
func (s *Service) generateBundleActivationEvents(t *Trace, r *answer.AnswerResult, grade gradeResult) {
	if t.PathType == retrieval.PathTypeWiki {
		return
	}
	var hits []retrieval.BundleHit
	if r.EvidenceSet != nil {
		hits = r.EvidenceSet.BundleHits
	}
	if len(hits) == 0 {
		return
	}

	directSet := make(map[string]bool, len(grade.DirectPointIDs))
	for _, pid := range grade.DirectPointIDs {
		directSet[pid] = true
	}
	var supportingEvidence []retrieval.Evidence
	if r.EvidenceSet != nil {
		supportingEvidence = r.EvidenceSet.Supporting
	}
	supportingFactsByPoint := citedFactIDsByEvidence(supportingEvidence, r.Citations)

	reason := "not_cited"
	switch {
	case r.Path == "error":
		reason = "answer_error"
	case t.RetrievalQuality == QualityGap:
		reason = "answer_gap"
	}

	for _, hit := range hits {
		bundleSuccess := false
		for _, pid := range hit.MemberPointIDs {
			// Same signal as generateActivationEvents' per-point judgment:
			// directSet[pid] (from grade.DirectPointIDs) or a supporting-role
			// citation. Deliberately not re-deriving from factsByPoint here —
			// directSet already is that derivation (directCitedPointIDs), so
			// checking both would be redundant, not additional evidence.
			memberSuccess := directSet[pid] || len(supportingFactsByPoint[pid]) > 0
			if memberSuccess {
				bundleSuccess = true
			}
			s.recordBundleMemberOutcome(t, hit.BundleID, pid, memberSuccess)
		}

		eventType := "activation_failure"
		fields := map[string]interface{}{
			"bundle_id":        hit.BundleID,
			"question_terms":   t.QuestionTerms,
			"match_score":      hit.MatchScore,
			"member_point_ids": nonNilStrings(hit.MemberPointIDs),
		}
		if bundleSuccess {
			eventType = "activation_success"
		} else {
			fields["reason"] = reason
		}
		payload, _ := json.Marshal(fields)
		slog.Debug("trace: generating bundle activation event", "trace_id", t.TraceID, "bundle_id", hit.BundleID, "event_type", eventType)
		if _, err := s.store.SaveLearningEvent(t.TraceID, eventType, string(payload)); err != nil {
			slog.Error("trace: save bundle activation event failed", "trace_id", t.TraceID, "bundle_id", hit.BundleID, "error", err)
		}
		s.recordBundleHitOutcome(t, hit, bundleSuccess)
	}
}

// recordBundleHitOutcome calls activation.RecordBundleOutcome using the
// hit's own stored quadruple — the Bundle-side mirror of recordHitOutcome.
func (s *Service) recordBundleHitOutcome(t *Trace, hit retrieval.BundleHit, success bool) {
	if s.enricher == nil {
		return
	}
	if err := s.enricher.RecordBundleOutcome(hit.BundleID, hit.Subject, hit.Intent, hit.Audience, hit.Constraint, success); err != nil {
		slog.Error("trace: record bundle outcome failed", "trace_id", t.TraceID, "bundle_id", hit.BundleID, "error", err)
	}
}

// recordBundleMemberOutcome calls activation.RecordMemberOutcome for a single
// member point resolved by a Bundle hit — the Bundle-side member axis.
func (s *Service) recordBundleMemberOutcome(t *Trace, bundleID, pointID string, success bool) {
	if s.enricher == nil {
		return
	}
	if err := s.enricher.RecordMemberOutcome(bundleID, pointID, success); err != nil {
		slog.Error("trace: record bundle member outcome failed", "trace_id", t.TraceID, "bundle_id", bundleID, "point_id", pointID, "error", err)
	}
}

// recordHitOutcome calls activation.RecordOutcome using the hit's own stored
// quadruple (not the query's — see activation.md「owning condition 的可判定性」
// and trace.md 步骤 3), for both role=direct and role=supporting outcomes with
// success=true — role no longer carries statistical weight, it's recorded in
// the event payload purely for display (docs/impl/v1/trace.md 步骤 3 payload
// 注释). A missing enricher (activation-less test setups) or a lookup failure
// is non-fatal — it must not interrupt the rest of trace_write.
func (s *Service) recordHitOutcome(t *Trace, hit retrieval.ActivationHit, success bool) {
	if s.enricher == nil {
		return
	}
	if err := s.enricher.RecordOutcome(hit.LinkID, hit.Subject, hit.Intent, hit.Audience, hit.Constraint, success, t.QuestionTerms); err != nil {
		slog.Error("trace: record outcome failed", "trace_id", t.TraceID, "link_id", hit.LinkID, "error", err)
	}
}

// citedFactIDsByEvidence maps each evidence point_id to the fact_ids Answer
// actually cited for it — the activation_success payload's cited_fact_ids
// (docs/impl/v1/trace.md payload 结构). Callers pass DirectEvidence for
// role="direct" or Supporting for role="supporting" (docs/impl/v1/trace.md
// 步骤 3: 命中且被引用即成功，角色只影响 study.md 的晋升权重，不影响是否
// 记为成功).
func citedFactIDsByEvidence(evidence []retrieval.Evidence, citations []string) map[string][]string {
	result := make(map[string][]string)
	citedSet := make(map[string]bool, len(citations))
	for _, fid := range citations {
		citedSet[fid] = true
	}
	for _, e := range evidence {
		if citedSet[e.FactID] {
			result[e.PointID] = append(result[e.PointID], e.FactID)
		}
	}
	return result
}

// citedUnitIDs returns the deduplicated unit_ids Answer actually cited —
// used by generateTopNCalibrationEvent's pool_rescued payload so Study's
// coefficient replay knows which pool candidates were the ones that had to
// land within N (docs/design/topn-coefficient-convergence.md 第 5 节).
func citedUnitIDs(es *retrieval.EvidenceSet, citations []string) []string {
	byFact := make(map[string]string, len(es.DirectEvidence)+len(es.Supporting))
	for _, e := range es.DirectEvidence {
		byFact[e.FactID] = e.UnitID
	}
	for _, e := range es.Supporting {
		byFact[e.FactID] = e.UnitID
	}
	seen := make(map[string]bool, len(citations))
	var out []string
	for _, fid := range citations {
		uid, ok := byFact[fid]
		if !ok || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}

func directEvidenceOf(es *retrieval.EvidenceSet) []retrieval.Evidence {
	if es == nil {
		return nil
	}
	return es.DirectEvidence
}

// generateTopNCalibrationEvent emits a topn_calibration learning_event when
// this trace's EvidenceSet carries a CompletenessClass — only slow/full-path
// traces that ran the pool-widen retry chain in
// answer.checkSlowPathSufficiency set this
// (docs/design/topn-coefficient-convergence.md 第 3 节). Study scans these
// events into topn_calibration_samples to compute top-N/目录检索系数的建议值
// (docs/impl/v1/topn-coefficient-convergence.md 阶段 B/C) — reporting only,
// not yet feeding any automatic adjustment.
func (s *Service) generateTopNCalibrationEvent(t *Trace, r *answer.AnswerResult, grade gradeResult) {
	es := r.EvidenceSet
	if es == nil || es.CompletenessClass == "" {
		return
	}

	rankProxyLower := 0
	rankProxyIsInterval := false
	switch es.CompletenessClass {
	case retrieval.CompletenessTight:
		if grade.CitedRankMax >= 0 {
			rankProxyLower = grade.CitedRankMax
		}
	case retrieval.CompletenessPoolRescued:
		rankProxyLower = es.WidenedToN
		rankProxyIsInterval = true
	}

	payload := map[string]interface{}{
		"n_at_query_time":           es.TopNAtBuild,
		"coefficient_at_query_time": es.CoefficientAtBuild,
		"completeness_class":        es.CompletenessClass,
		"rank_proxy_lower":          rankProxyLower,
		"rank_proxy_is_interval":    rankProxyIsInterval,
		"candidate_pool_size":       es.CandidatePoolSize,
	}
	if es.CompletenessClass == retrieval.CompletenessPoolRescued {
		if pool := es.CalibrationPoolSnapshot(es.WidenedToN); len(pool) > 0 {
			payload["pool_snapshot"] = pool
		}
		payload["cited_unit_ids"] = citedUnitIDs(es, r.Citations)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Error("trace: marshal topn_calibration payload failed", "trace_id", t.TraceID, "error", err)
		return
	}
	if _, err := s.store.SaveLearningEvent(t.TraceID, "topn_calibration", string(payloadJSON)); err != nil {
		slog.Error("trace: save topn_calibration event failed", "trace_id", t.TraceID, "error", err)
	}
}

func (s *Service) generateLearningEvents(t *Trace, r *answer.AnswerResult) {
	if t.RetrievalQuality == QualityGap {
		reason := gapReason(r)
		slog.Debug("trace: generating knowledge_gap event", "trace_id", t.TraceID, "question", t.Question, "reason", reason)
		payload, _ := json.Marshal(map[string]string{"question": t.Question, "reason": reason})
		if _, err := s.store.SaveLearningEvent(t.TraceID, "knowledge_gap", string(payload)); err != nil {
			slog.Error("trace: save knowledge_gap event failed", "trace_id", t.TraceID, "error", err)
		}
	} else {
		slog.Debug("trace: no learning event needed", "trace_id", t.TraceID, "quality", t.RetrievalQuality)
	}
}

// gapReason implements docs/impl/v1/trace.md's knowledge_gap payload.reason
// derivation: an LLM generation failure takes priority over whatever
// retrieval found (or didn't), since it means the gap isn't really about
// missing knowledge; otherwise defer to EvidenceSet.GapReason (set by
// retrieval — see docs/impl/v1/retrieval.md 步骤 6).
func gapReason(r *answer.AnswerResult) string {
	if r.Path == "error" {
		return "answer_error"
	}
	if r.EvidenceSet != nil && r.EvidenceSet.GapReason != "" {
		return r.EvidenceSet.GapReason
	}
	return "unspecified"
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
		if _, err := s.store.SaveLearningEvent(t.TraceID, "user_correction", string(payload)); err != nil {
			slog.Error("trace: save user_correction event failed", "trace_id", t.TraceID, "error", err)
		}

		// correction_weight-weighted negative signal — the direct replacement
		// for Study's removed window-based weakening (docs/impl/v1/trace.md
		// 步骤 4). Uses the trace's own stored query quadruple, not a
		// re-derived Match() owning condition — a known, accepted
		// approximation for this low-frequency, human-triggered path.
		if s.enricher != nil && len(t.ActivationLinkIDs) > 0 {
			for _, linkID := range t.ActivationLinkIDs {
				for i := 0; i < s.correctionWeight; i++ {
					if err := s.enricher.RecordOutcome(linkID, t.Subject, t.Intent, t.Audience, t.ConstraintText, false, ""); err != nil {
						slog.Error("trace: record correction outcome failed", "trace_id", t.TraceID, "link_id", linkID, "error", err)
					}
				}
			}
		}
	}

	return nil
}

// WriteAuditOutcome is the exported entry point Retrieval's background
// audit-trial orchestration (docs/impl/v1/retrieval.md 步骤 2c, 阶段 4) calls
// through the retrieval.AuditOutcomeWriter interface. Retrieval's trigger
// point (inside tryFastPath, after the fast-path answer has already been
// handed back to the caller) has no real trace_id to attach the resulting
// activation_audit_success/failure learning_event to — a trace only gets
// created later, downstream, once Answer finishes generating and calls
// ProcessTrace. Rather than changing that ordering (which would delay the
// user-facing response on an audit trial, defeating the point), this creates
// a minimal, inert placeholder answers/traces row pair
// (Store.SaveAuditPlaceholder) purely to satisfy learning_events.trace_id's
// FK, then delegates to the existing (Phase 2) writeAuditOutcome unchanged.
// matchScore is recorded as 0 since the interface (by design, mirroring
// unit.ActivationNotifier/activation.WikiNotifier's cross-package
// notification shape) doesn't carry it — the hit's own match_score isn't
// needed to compute agreement, only its point_id/four-tuple are.
func (s *Service) WriteAuditOutcome(linkID, pointID, subject, intent, audience, constraint string, agree bool, slowPathDirectPointIDs []string) error {
	traceID, err := s.store.SaveAuditPlaceholder(pointID, subject, intent, audience, constraint)
	if err != nil {
		slog.Error("trace: create audit placeholder trace failed", "link_id", linkID, "point_id", pointID, "error", err)
		return err
	}
	return s.writeAuditOutcome(traceID, linkID, pointID, subject, intent, audience, constraint, 0, agree, slowPathDirectPointIDs)
}

// writeAuditOutcome records the result of an independent verification trial
// (fast-path vs. slow-path comparison, docs/impl/v1/trace.md 步骤 3b) as an
// activation_audit_success / activation_audit_failure learning_event, then
// calls activation.RecordAuditOutcome so the audited condition's
// audited_success_count/audited_failure_count (and the underlying
// success_count/failure_count) advance together. Called from
// WriteAuditOutcome (阶段 4, Retrieval's background audit-trial orchestration).
func (s *Service) writeAuditOutcome(traceID, linkID, pointID, subject, intent, audience, constraint string, matchScore float64, agree bool, slowPathDirectPointIDs []string) error {
	eventType := "activation_audit_success"
	fields := map[string]interface{}{
		"link_id":                    linkID,
		"point_id":                   pointID,
		"subject":                    subject,
		"intent":                     intent,
		"audience":                   audience,
		"constraint":                 constraint,
		"match_score":                matchScore,
		"audited_trace_id":           traceID,
		"slow_path_direct_point_ids": nonNilStrings(slowPathDirectPointIDs),
		"agree":                      agree,
	}
	if !agree {
		eventType = "activation_audit_failure"
		fields["reason"] = "point_not_in_slow_path"
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("trace: marshal %s payload: %w", eventType, err)
	}

	if _, err := s.store.SaveLearningEvent(traceID, eventType, string(payload)); err != nil {
		slog.Error("trace: save audit outcome event failed", "trace_id", traceID, "link_id", linkID, "event_type", eventType, "error", err)
		return err
	}

	if s.enricher != nil {
		if err := s.enricher.RecordAuditOutcome(linkID, subject, intent, audience, constraint, agree); err != nil {
			slog.Error("trace: record audit outcome failed", "trace_id", traceID, "link_id", linkID, "error", err)
		}
	}

	return nil
}

// SynthesisOutcomeWriter is implemented by *wiki.Service — mirrors
// ObservedConditionEnricher's cross-package shape (interface defined here in
// the consumer package, wiki satisfies it structurally, main.go wires the
// two with a setter) — the consumption-side hand-off for Wiki's synthesis-
// satisfaction axis (docs/impl/v1/wiki.md 步骤 4a).
type SynthesisOutcomeWriter interface {
	RecordSynthesisOutcome(pageID string, agree bool) error
}

// SetSynthesisOutcomeWriter wires the synthesis-outcome hand-off target
// (usually *wiki.Service). Unset means WriteSynthesisOutcome still writes
// its learning_event but skips updating wiki_pages' counters — nil-safe,
// mirrors SetObservedConditionEnricher's optionality.
func (s *Service) SetSynthesisOutcomeWriter(w SynthesisOutcomeWriter) {
	s.synthesisWriter = w
}

// WriteSynthesisOutcome is the entry point Retrieval's background synthesis-
// audit-trial orchestration (docs/impl/v1/wiki.md 步骤 4a, reusing
// retrieval.md 步骤 2c's exact orchestration shape) calls after a Wiki
// direct answer has already been served and independently re-verified via a
// forced slow-path retrieval. Audit-only by design — there is no
// self-graded tier for the synthesis axis (wiki.md 步骤 4a「未中选」) — so
// every call here both writes a wiki_synthesis_audit_success/failure
// learning_event and, via SynthesisOutcomeWriter, advances the page's
// synthesis_{success,failure}_count and synthesis_audited_{success,failure}_count
// together (never independently, unlike ActivationLink/Bundle's
// RecordAuditOutcome).
//
// No real trace_id exists for this background trial either (same reasoning
// as WriteAuditOutcome) — reuses the same SaveAuditPlaceholder mechanism to
// satisfy learning_events.trace_id's FK.
func (s *Service) WriteSynthesisOutcome(pageID, auditedTraceQuestion string, slowPathDirectPointIDs []string, agree bool) error {
	traceID, err := s.store.SaveAuditPlaceholder(auditedTraceQuestion, "", "", "", "")
	if err != nil {
		slog.Error("trace: create synthesis audit placeholder trace failed", "page_id", pageID, "error", err)
		return err
	}

	eventType := "wiki_synthesis_audit_success"
	fields := map[string]interface{}{
		"page_id":                    pageID,
		"audited_trace_id":           traceID,
		"slow_path_direct_point_ids": nonNilStrings(slowPathDirectPointIDs),
		"agree":                      agree,
	}
	if !agree {
		eventType = "wiki_synthesis_audit_failure"
		fields["reason"] = "point_not_in_page_scope"
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("trace: marshal %s payload: %w", eventType, err)
	}

	if _, err := s.store.SaveLearningEvent(traceID, eventType, string(payload)); err != nil {
		slog.Error("trace: save synthesis outcome event failed", "trace_id", traceID, "page_id", pageID, "event_type", eventType, "error", err)
		return err
	}

	if s.synthesisWriter != nil {
		if err := s.synthesisWriter.RecordSynthesisOutcome(pageID, agree); err != nil {
			slog.Error("trace: record synthesis outcome failed", "trace_id", traceID, "page_id", pageID, "error", err)
		}
	}

	return nil
}
