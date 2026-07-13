package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jxman78/wiki-brain/internal/rerank"
)

const (
	defaultRerankExtractBatchMaxChars = 4000
	defaultRerankExtractConcurrency   = 2
)

type rerankSemanticCandidate struct {
	id      string
	content string
}

type rerankSemanticExtractionResult struct {
	Results []rerankSemanticExtraction `json:"results"`
}

type rerankSemanticExtraction struct {
	UnitID       string   `json:"unit_id"`
	SourceTheme  string   `json:"source_theme"`
	ContentTheme string   `json:"content_theme"`
	Intent       string   `json:"intent"`
	Object       string   `json:"object"`
	Scope        string   `json:"scope"`
	KeyFacts     []string `json:"key_facts"`
}

func (s *Service) extractRerankSemantics(
	ctx context.Context,
	sourceTitle string,
	mdLines []string,
	pool []unitCandidate,
) (map[string]rerank.Semantics, error) {
	if len(pool) == 0 {
		return map[string]rerank.Semantics{}, nil
	}

	candidates := make([]rerankSemanticCandidate, 0, len(pool))
	seenIDs := make(map[string]bool, len(pool))
	for _, candidate := range pool {
		if candidate.id == "" {
			return nil, fmt.Errorf("unit: rerank semantics candidate has empty unit_id")
		}
		if seenIDs[candidate.id] {
			return nil, fmt.Errorf("unit: rerank semantics duplicate unit_id: %s", candidate.id)
		}
		if candidate.lineStart < 1 || candidate.lineEnd < candidate.lineStart || candidate.lineEnd > len(mdLines) {
			return nil, fmt.Errorf("unit: rerank semantics invalid range for unit_id %s: %d-%d", candidate.id, candidate.lineStart, candidate.lineEnd)
		}
		seenIDs[candidate.id] = true
		candidates = append(candidates, rerankSemanticCandidate{
			id:      candidate.id,
			content: strings.Join(mdLines[candidate.lineStart-1:candidate.lineEnd], "\n"),
		})
	}

	batches := splitRerankSemanticBatches(candidates, s.rerankExtractBatchMaxChars())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, s.rerankExtractConcurrency())
	semantics := make(map[string]rerank.Semantics, len(pool))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var errOnce sync.Once

scheduling:
	for _, batch := range batches {
		batch := batch
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break scheduling
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			batchSemantics, err := s.extractRerankSemanticBatch(ctx, sourceTitle, batch)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}

			mu.Lock()
			for unitID, semantic := range batchSemantics {
				semantics[unitID] = semantic
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(semantics) != len(pool) {
		return nil, fmt.Errorf("unit: rerank semantics result coverage = %d, want %d", len(semantics), len(pool))
	}
	return semantics, nil
}

func (s *Service) extractRerankSemanticBatch(ctx context.Context, sourceTitle string, batch []rerankSemanticCandidate) (map[string]rerank.Semantics, error) {
	resp, err := s.llmClient.CompleteJSON(ctx, "unit_semantics_extract.md", map[string]string{
		"source_title": sourceTitle,
		"units":        formatRerankSemanticUnits(batch),
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("unit: rerank semantics llm: %w", err)
	}

	var result rerankSemanticExtractionResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("unit: rerank semantics parse: %w", err)
	}

	expected := make(map[string]bool, len(batch))
	for _, candidate := range batch {
		expected[candidate.id] = true
	}
	semantics := make(map[string]rerank.Semantics, len(result.Results))
	for _, extracted := range result.Results {
		if !expected[extracted.UnitID] {
			return nil, fmt.Errorf("unit: rerank semantics returned unknown unit_id: %s", extracted.UnitID)
		}
		if _, exists := semantics[extracted.UnitID]; exists {
			return nil, fmt.Errorf("unit: rerank semantics returned duplicate unit_id: %s", extracted.UnitID)
		}
		semantics[extracted.UnitID] = rerank.Semantics{
			UnitID:        extracted.UnitID,
			SourceTheme:   extracted.SourceTheme,
			ContentTheme:  extracted.ContentTheme,
			Intent:        extracted.Intent,
			Object:        extracted.Object,
			Scope:         extracted.Scope,
			KeyFacts:      extracted.KeyFacts,
			PromptVersion: rerank.ExtractPromptVersion,
		}
	}
	for _, candidate := range batch {
		if _, exists := semantics[candidate.id]; !exists {
			return nil, fmt.Errorf("unit: rerank semantics omitted unit_id: %s", candidate.id)
		}
	}
	return semantics, nil
}

func (s *Service) rerankExtractBatchMaxChars() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankExtractBatchMaxChars > 0 {
		return s.cfg.Retrieval.RerankExtractBatchMaxChars
	}
	return defaultRerankExtractBatchMaxChars
}

func (s *Service) rerankExtractConcurrency() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankExtractConcurrency > 0 {
		return s.cfg.Retrieval.RerankExtractConcurrency
	}
	return defaultRerankExtractConcurrency
}

func splitRerankSemanticBatches(candidates []rerankSemanticCandidate, maxChars int) [][]rerankSemanticCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankExtractBatchMaxChars
	}

	var batches [][]rerankSemanticCandidate
	var current []rerankSemanticCandidate
	currentChars := 0
	for _, candidate := range candidates {
		itemChars := utf8.RuneCountInString(candidate.content)
		if len(current) > 0 && currentChars+itemChars > maxChars {
			batches = append(batches, current)
			current = nil
			currentChars = 0
		}
		current = append(current, candidate)
		currentChars += itemChars
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func formatRerankSemanticUnits(candidates []rerankSemanticCandidate) string {
	var out strings.Builder
	for _, candidate := range candidates {
		fmt.Fprintf(&out, "[%s] %s\n\n", candidate.id, candidate.content)
	}
	return out.String()
}
