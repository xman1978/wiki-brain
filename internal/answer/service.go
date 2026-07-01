package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

var reasoningKeywords = []string{
	"如果", "假设", "假定", "假如", "若是",
	"多少", "计算", "算出",
	"比较", "对比", "哪个更",
	"为什么", "原因是什么",
	"经历了哪些", "哪些阶段", "什么流程",
	"应该怎么", "如何处理",
}

type Service struct {
	store     *Store
	llmClient llm.LLMClient
	queue     *queue.Queue
	retSvc    *retrieval.Service
}

func NewService(store *Store, llmClient llm.LLMClient, q *queue.Queue, retSvc *retrieval.Service) *Service {
	return &Service{
		store:     store,
		llmClient: llmClient,
		queue:     q,
		retSvc:    retSvc,
	}
}

func (s *Service) Answer(ctx context.Context, es *retrieval.EvidenceSet) *AnswerResult {
	hasEvidence := len(es.DirectEvidence) > 0 || len(es.Supporting) > 0

	if !hasEvidence {
		return s.handleNone(ctx, es)
	}

	path := es.Path
	if path == "short" && needsReasoning(es.Question) {
		path = "deep"
		slog.Info("answer: question complexity upgraded to deep", "question", es.Question)
	}

	switch path {
	case "short":
		return s.handleGenerate(ctx, es, "short", "answer_short.md", "default")
	case "deep":
		return s.handleGenerate(ctx, es, "deep", "answer_deep.md", "reasoning")
	default:
		return s.handleGenerate(ctx, es, path, "answer_deep.md", "reasoning")
	}
}

func (s *Service) AnswerWithDeep(ctx context.Context, es *retrieval.EvidenceSet, forceDeep bool) *AnswerResult {
	if forceDeep {
		hasEvidence := len(es.DirectEvidence) > 0 || len(es.Supporting) > 0
		if hasEvidence {
			es.Path = "deep"
		}
	}
	return s.Answer(ctx, es)
}

func (s *Service) AnswerFromQuestion(ctx context.Context, question string, forceDeep bool) (*AnswerResult, error) {
	start := time.Now()
	slog.Debug("answer: start", "question", question, "force_deep", forceDeep)

	retrievalStart := time.Now()
	es, err := s.retSvc.Retrieve(ctx, question)
	if err != nil {
		slog.Debug("answer: retrieval failed", "duration_ms", time.Since(retrievalStart).Milliseconds(), "error", err)
		return nil, fmt.Errorf("answer: retrieval failed: %w", err)
	}
	slog.Debug("answer: retrieval done",
		"duration_ms", time.Since(retrievalStart).Milliseconds(),
		"direct_count", len(es.DirectEvidence),
		"supporting_count", len(es.Supporting),
		"path", es.Path)

	result := s.AnswerWithDeep(ctx, es, forceDeep)
	slog.Debug("answer: complete",
		"answer_id", result.AnswerID,
		"path", result.Path,
		"has_answer", result.HasAnswer,
		"citation_count", len(result.Citations),
		"total_ms", time.Since(start).Milliseconds())
	return result, nil
}

// AnswerStream does retrieval then streams LLM output. The returned channel
// emits StreamChunk (thinking/content/done/error). After ChunkDone the caller
// can read the final AnswerResult via the callback.
func (s *Service) AnswerStream(ctx context.Context, question string, forceDeep bool, subject, intent, audience, constraint string) (<-chan llm.StreamChunk, func() *AnswerResult, error) {
	outCh := make(chan llm.StreamChunk, 32)
	var finalResult *AnswerResult

	go func() {
		defer close(outCh)
		totalStart := time.Now()
		slog.Debug("answer: stream start", "question", question, "force_deep", forceDeep)

		outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: `{"phase":"retrieval","status":"start"}`}
		retrievalStart := time.Now()
		slog.Debug("answer: stream retrieval start")

		progress := func(evt retrieval.ProgressEvent) {
			data := fmt.Sprintf(`{"phase":"%s","status":"%s","detail":"%s","duration_ms":%d}`,
				evt.Phase, evt.Status, evt.Detail, evt.Duration)
			outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: data}
		}

		es, err := s.retSvc.RetrieveWithProgress(ctx, retrieval.QueryContext{
			Question:   question,
			Subject:    subject,
			Intent:     intent,
			Audience:   audience,
			Constraint: constraint,
		}, progress)
		if err != nil {
			slog.Error("answer: retrieval failed", "error", err)
			slog.Debug("answer: stream retrieval error", "duration_ms", time.Since(retrievalStart).Milliseconds(), "error", err)
			outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
				`{"phase":"retrieval","status":"error","duration_ms":%d}`, time.Since(retrievalStart).Milliseconds())}
			outCh <- llm.StreamChunk{Type: llm.ChunkError, Err: err}
			return
		}

		retrievalMs := time.Since(retrievalStart).Milliseconds()
		slog.Debug("answer: stream retrieval done",
			"duration_ms", retrievalMs,
			"direct_count", len(es.DirectEvidence),
			"supporting_count", len(es.Supporting),
			"conflict_count", len(es.Conflicts),
			"path", es.Path)
		for i, e := range es.DirectEvidence {
			slog.Debug("answer: stream direct evidence",
				"index", i, "fact_id", e.FactID, "point_id", e.PointID, "unit_id", e.UnitID)
		}
		for i, e := range es.Supporting {
			slog.Debug("answer: stream supporting evidence",
				"index", i, "fact_id", e.FactID, "point_id", e.PointID, "unit_id", e.UnitID)
		}
		outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
			`{"phase":"retrieval","status":"done","duration_ms":%d,"direct_count":%d,"supporting_count":%d,"path":"%s"}`,
			retrievalMs, len(es.DirectEvidence), len(es.Supporting), es.Path)}

		if forceDeep {
			hasEvidence := len(es.DirectEvidence) > 0 || len(es.Supporting) > 0
			if hasEvidence {
				slog.Debug("answer: stream force deep override", "original_path", es.Path)
				es.Path = "deep"
			}
		}

		hasEvidence := len(es.DirectEvidence) > 0 || len(es.Supporting) > 0
		if !hasEvidence {
			totalMs := time.Since(totalStart).Milliseconds()
			slog.Debug("answer: stream no evidence", "total_ms", totalMs)
			outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
				`{"phase":"answer","status":"no_evidence","total_ms":%d}`, totalMs)}
			finalResult = s.handleNone(ctx, es)
			outCh <- llm.StreamChunk{Type: llm.ChunkContent, Content: finalResult.Content}
			outCh <- llm.StreamChunk{Type: llm.ChunkDone}
			return
		}

		path := es.Path
		if path == "short" && needsReasoning(es.Question) {
			path = "deep"
			slog.Debug("answer: stream complexity upgraded to deep", "question", es.Question)
		}

		promptFile := "answer_short.md"
		model := "default"
		if path == "deep" {
			promptFile = "answer_deep.md"
			model = "reasoning"
		}

		slog.Debug("answer: stream generation start",
			"path", path, "prompt_file", promptFile, "model", model)
		outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
			`{"phase":"generation","status":"start","path":"%s","model":"%s"}`, path, model)}
		generationStart := time.Now()

		vars := buildPromptVars(es)
		llmCh, err := s.llmClient.CompleteStream(ctx, promptFile, vars, model)
		if err != nil {
			genMs := time.Since(generationStart).Milliseconds()
			slog.Error("answer: stream LLM failed", "error", err)
			slog.Debug("answer: stream LLM init error", "duration_ms", genMs, "error", err)
			outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
				`{"phase":"generation","status":"error","duration_ms":%d}`, genMs)}
			finalResult = s.handleError(es)
			outCh <- llm.StreamChunk{Type: llm.ChunkContent, Content: finalResult.Content}
			outCh <- llm.StreamChunk{Type: llm.ChunkDone}
			return
		}

		slog.Debug("answer: stream LLM channel opened, waiting for chunks")
		var contentBuf strings.Builder
		chunkCount := 0
		for chunk := range llmCh {
			chunkCount++
			switch chunk.Type {
			case llm.ChunkThinking, llm.ChunkContent:
				if chunk.Type == llm.ChunkContent {
					contentBuf.WriteString(chunk.Content)
				}
				outCh <- chunk
			case llm.ChunkError:
				slog.Error("answer: stream error", "error", chunk.Err)
				slog.Debug("answer: stream chunk error", "chunk_index", chunkCount, "error", chunk.Err)
				finalResult = s.handleError(es)
				outCh <- llm.StreamChunk{Type: llm.ChunkError, Err: chunk.Err}
				return
			case llm.ChunkDone:
				raw := contentBuf.String()
				raw = stripThinkTags(raw)
				jsonStr := llm.ExtractAndRepairJSON(raw)
				slog.Debug("answer: stream LLM output received",
					"chunk_count", chunkCount,
					"raw_len", len(raw),
					"json_len", len(jsonStr))

				var output llmAnswerOutput
				if err := json.Unmarshal([]byte(jsonStr), &output); err != nil {
					slog.Error("answer: stream parse failed", "error", err)
					slog.Debug("answer: stream parse error", "json_preview", truncateStr(jsonStr, 200))
					finalResult = s.handleError(es)
					outCh <- llm.StreamChunk{Type: llm.ChunkDone}
					return
				}

				citations := validateCitations(output.Citations, es)
				finalResult = &AnswerResult{
					AnswerID:    uuid.New().String(),
					Question:    es.Question,
					Content:     output.Content,
					Citations:   citations,
					HasAnswer:   true,
					Path:        path,
					EvidenceSet: es,
				}
				s.saveAndEnqueue(finalResult, "v1", model)
				generationMs := time.Since(generationStart).Milliseconds()
				totalMs := time.Since(totalStart).Milliseconds()
				slog.Debug("answer: stream complete",
					"answer_id", finalResult.AnswerID,
					"path", path,
					"citation_count", len(citations),
					"content_len", len(output.Content),
					"generation_ms", generationMs,
					"total_ms", totalMs)
				outCh <- llm.StreamChunk{Type: llm.ChunkPhase, Content: fmt.Sprintf(
					`{"phase":"generation","status":"done","duration_ms":%d,"total_ms":%d}`, generationMs, totalMs)}
				outCh <- llm.StreamChunk{Type: llm.ChunkDone}
				return
			}
		}
	}()

	return outCh, func() *AnswerResult { return finalResult }, nil
}

func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(s, "</think>")
		if end == -1 {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}


func (s *Service) handleNone(_ context.Context, es *retrieval.EvidenceSet) *AnswerResult {
	r := &AnswerResult{
		AnswerID:    uuid.New().String(),
		Question:    es.Question,
		Content:     "知识库中暂无相关材料，无法回答该问题。",
		Citations:   []string{},
		HasAnswer:   false,
		Path:        "none",
		EvidenceSet: es,
	}
	s.saveAndEnqueue(r, "", "")
	return r
}

func (s *Service) handleGenerate(ctx context.Context, es *retrieval.EvidenceSet, path, promptFile, model string) *AnswerResult {
	slog.Debug("answer: generation start",
		"path", path, "prompt_file", promptFile, "model", model, "question", es.Question)
	genStart := time.Now()
	vars := buildPromptVars(es)

	raw, err := s.llmClient.CompleteJSON(ctx, promptFile, vars, model)
	if err != nil {
		slog.Warn("answer: first LLM attempt failed, retrying", "path", path, "error", err)
		raw, err = s.llmClient.CompleteJSON(ctx, promptFile, vars, model)
	}
	if err != nil {
		slog.Error("answer: LLM failed after retry", "path", path, "question", es.Question, "error", err)
		return s.handleError(es)
	}
	slog.Debug("answer: LLM response received",
		"path", path, "duration_ms", time.Since(genStart).Milliseconds(), "response_len", len(raw))

	var output llmAnswerOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		slog.Error("answer: parse LLM output failed", "path", path, "error", err)
		return s.handleError(es)
	}

	citations := validateCitations(output.Citations, es)
	slog.Debug("answer: generation done",
		"path", path,
		"citation_count", len(citations),
		"content_len", len(output.Content),
		"duration_ms", time.Since(genStart).Milliseconds())

	r := &AnswerResult{
		AnswerID:    uuid.New().String(),
		Question:    es.Question,
		Content:     output.Content,
		Citations:   citations,
		HasAnswer:   true,
		Path:        path,
		EvidenceSet: es,
	}
	s.saveAndEnqueue(r, "v1", model)
	return r
}

func (s *Service) handleError(es *retrieval.EvidenceSet) *AnswerResult {
	r := &AnswerResult{
		AnswerID:    uuid.New().String(),
		Question:    es.Question,
		Content:     "回答生成失败，请稍后重试。",
		Citations:   []string{},
		HasAnswer:   false,
		Path:        "error",
		EvidenceSet: es,
	}
	s.saveAndEnqueue(r, "", "")
	return r
}

func (s *Service) saveAndEnqueue(r *AnswerResult, promptVersion, modelName string) {
	slog.Debug("answer: saving to db", "answer_id", r.AnswerID, "path", r.Path, "has_answer", r.HasAnswer)
	if err := s.store.Save(r, promptVersion, modelName); err != nil {
		slog.Error("answer: save failed", "answer_id", r.AnswerID, "error", err)
	}

	slog.Debug("answer: enqueuing trace task", "answer_id", r.AnswerID)
	ok := s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeTrace,
		Payload: &queue.TraceTask{Result: r},
	})
	if !ok {
		slog.Error("answer: trace enqueue failed, queue full", "answer_id", r.AnswerID)
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func needsReasoning(question string) bool {
	for _, kw := range reasoningKeywords {
		if strings.Contains(question, kw) {
			return true
		}
	}
	return false
}

func buildPromptVars(es *retrieval.EvidenceSet) map[string]string {
	constraint := es.Constraint
	if constraint == "" {
		constraint = "（无）"
	}
	vars := map[string]string{
		"question":   es.Question,
		"constraint": constraint,
	}

	var directLines []string
	for _, e := range es.DirectEvidence {
		directLines = append(directLines, fmt.Sprintf("[%s] %s", e.FactID, e.Content))
	}
	vars["direct_evidence_list"] = strings.Join(directLines, "\n")

	var supportLines []string
	for _, e := range es.Supporting {
		supportLines = append(supportLines, fmt.Sprintf("[%s] %s", e.FactID, e.Content))
	}
	vars["supporting_evidence_list"] = strings.Join(supportLines, "\n")

	return vars
}

func validateCitations(citations []string, es *retrieval.EvidenceSet) []string {
	validIDs := make(map[string]bool)
	for _, e := range es.DirectEvidence {
		validIDs[e.FactID] = true
	}
	for _, e := range es.Supporting {
		validIDs[e.FactID] = true
	}

	var hallucinated []string
	var valid []string
	for _, id := range citations {
		if validIDs[id] {
			valid = append(valid, id)
		} else {
			hallucinated = append(hallucinated, id)
		}
	}

	if len(hallucinated) > 0 {
		slog.Warn("answer: filtered hallucinated fact_ids",
			"hallucinated", hallucinated,
			"question", es.Question,
		)
	}

	if valid == nil {
		valid = []string{}
	}
	return valid
}
