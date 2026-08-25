package retrieval

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// judgeBatchCoverageRetries bounds how many times a single batch is re-sent
// to the LLM when its response silently omits an item_id that was in the
// input — same completeness concern rerankJudgeCoverageRetries guards
// against (see its comment), generalized so every per-item LLM classification
// step (rerank relevance/classify, source filter, outline filter) gets the
// same protection instead of each reinventing it.
const judgeBatchCoverageRetries = 2

// splitJudgeBatches packs items into batches bounded by maxChars (total
// JSON-encoded size per batch) and maxCandidates (item count per batch) —
// greedy bin-packing, largest items first, so no single batch's prompt can
// grow unbounded as the candidate pool grows. Shared packing core behind
// splitRerankJudgeBatches and any other per-item LLM classification step
// (source filter, outline filter) that needs the same bounded-batch-size
// property.
func splitJudgeBatches[T any](items []T, maxChars, maxCandidates int) [][]T {
	if len(items) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankJudgeBatchMaxChars
	}
	if maxCandidates <= 0 {
		maxCandidates = defaultRerankJudgeBatchMaxCandidates
	}

	type sizedItem struct {
		item  T
		chars int
	}
	sized := make([]sizedItem, len(items))
	totalChars := 0
	for i, it := range items {
		itemJSON, _ := json.Marshal(it)
		n := utf8.RuneCount(itemJSON)
		sized[i] = sizedItem{item: it, chars: n}
		totalChars += n
	}
	sort.SliceStable(sized, func(i, j int) bool { return sized[i].chars > sized[j].chars })

	numBatches := (totalChars + maxChars - 1) / maxChars
	if byCount := (len(items) + maxCandidates - 1) / maxCandidates; byCount > numBatches {
		numBatches = byCount
	}
	if numBatches < 1 {
		numBatches = 1
	}
	batches := make([][]T, numBatches)
	batchChars := make([]int, numBatches)

	for _, si := range sized {
		best := -1
		for b := 0; b < len(batches); b++ {
			if len(batches[b]) >= maxCandidates {
				continue
			}
			if len(batches[b]) > 0 && batchChars[b]+si.chars > maxChars {
				continue
			}
			if best == -1 || batchChars[b] < batchChars[best] {
				best = b
			}
		}
		if best == -1 {
			batches = append(batches, nil)
			batchChars = append(batchChars, 0)
			best = len(batches) - 1
		}
		batches[best] = append(batches[best], si.item)
		batchChars[best] += si.chars
	}

	nonEmpty := make([][]T, 0, len(batches))
	for _, b := range batches {
		if len(b) > 0 {
			nonEmpty = append(nonEmpty, b)
		}
	}
	return nonEmpty
}

// missingJudgeIDs returns the item ids (via idOf) present in batch but absent
// from results.
func missingJudgeIDs[T any](batch []T, results map[string]string, idOf func(T) string) []string {
	var missing []string
	for _, it := range batch {
		if _, ok := results[idOf(it)]; !ok {
			missing = append(missing, idOf(it))
		}
	}
	return missing
}

// runJudgeBatches is the generic fan-out shared by every per-item LLM
// classification step in this package (rerank relevance/classify, source
// filter, outline filter): split items into bounded batches, run them with
// bounded concurrency, retry a batch that silently drops an item_id from its
// response, default any item still missing after retries, and merge into one
// result map. callBatch judges one batch and returns per-item string values
// (caller-defined meaning — role, "relevant"/"irrelevant", etc). Any batch
// that still errors after retries cancels the remaining batches and returns
// the error — callers that want fail-open behavior (e.g. "keep every
// candidate" when the LLM step is just a coarse pre-filter) catch that error
// at the call site and fall back, same as before this was generalized.
func runJudgeBatches[T any](ctx context.Context, items []T, maxChars, maxCandidates, concurrency int, idOf func(T) string, defaultForMissing string, callBatch func(ctx context.Context, batch []T) (map[string]string, error)) (map[string]string, error) {
	batches := splitJudgeBatches(items, maxChars, maxCandidates)
	if len(batches) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = defaultRerankJudgeConcurrency
	}
	batchSizes := make([]int, len(batches))
	for i, b := range batches {
		batchSizes[i] = len(b)
	}
	slog.Info("retrieval: judge batches", "batch_count", len(batches), "batch_sizes", batchSizes, "concurrency", concurrency)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make(map[string]string)
	var firstErr error
	var errOnce sync.Once

launchBatches:
	for i, batch := range batches {
		i, batch := i, batch
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launchBatches
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			batchStart := time.Now()
			var batchResults map[string]string
			var err error
			var missing []string
			for attempt := 0; attempt < judgeBatchCoverageRetries; attempt++ {
				batchResults, err = callBatch(ctx, batch)
				if err != nil {
					break
				}
				missing = missingJudgeIDs(batch, batchResults, idOf)
				if len(missing) == 0 {
					break
				}
				slog.Warn("retrieval: judge batch missing item_id(s) in response, retrying",
					"batch_index", i, "attempt", attempt, "missing", missing)
			}
			batchMs := time.Since(batchStart).Milliseconds()
			if err != nil {
				slog.Info("retrieval: judge batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs, "error", err)
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			if len(missing) > 0 {
				slog.Warn("retrieval: judge batch still missing item_id(s) after retries, defaulting",
					"batch_index", i, "missing", missing, "default", defaultForMissing)
				for _, id := range missing {
					batchResults[id] = defaultForMissing
				}
			}
			slog.Info("retrieval: judge batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs)
			mu.Lock()
			for id, v := range batchResults {
				results[id] = v
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
	return results, nil
}
