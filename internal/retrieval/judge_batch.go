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
//
// groupKeyOf, when non-nil, packs at the group level instead of the item
// level: items sharing a non-empty key are kept in the same batch whenever
// they jointly fit maxChars/maxCandidates (a group larger than maxCandidates
// is chunked into maxCandidates-sized pieces, each still pure to that group,
// rather than dissolving into singletons that would mix back in with other
// groups — a bigger prompt budget can't be manufactured just to keep the
// whole group in one batch). This exists for rerank's same-object judging
// pool (2026-08-26 决策，见 docs/impl/v1/retrieval.md 编注): candidates
// sharing the same object/scope value — regardless of which KU they came
// from — should see each other during judging and not be interleaved with
// candidates carrying unrelated objects, which is what let the model
// rationalize a wrong object-match verdict in production (see
// judgeRerankTwoStep's reconcileSiblingConsistency doc comment for the case).
// Items with an empty group key (groupKeyOf returns "") are never grouped
// with each other — each is its own singleton group, same as passing
// groupKeyOf=nil. Passing nil preserves the exact previous per-item packing
// behavior for callers (source filter, outline filter) that have no
// grouping concept.
func splitJudgeBatches[T any](items []T, maxChars, maxCandidates int, groupKeyOf func(T) string) [][]T {
	if len(items) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankJudgeBatchMaxChars
	}
	if maxCandidates <= 0 {
		maxCandidates = defaultRerankJudgeBatchMaxCandidates
	}

	type group struct {
		items []T
		chars int
		key   string // "" = no grouping signal, free to share a batch with anything
	}
	var groups []group
	if groupKeyOf == nil {
		groups = make([]group, len(items))
		for i, it := range items {
			itemJSON, _ := json.Marshal(it)
			groups[i] = group{items: []T{it}, chars: utf8.RuneCount(itemJSON)}
		}
	} else {
		byKey := make(map[string]*group)
		var order []string
		for _, it := range items {
			key := groupKeyOf(it)
			itemJSON, _ := json.Marshal(it)
			n := utf8.RuneCount(itemJSON)
			if key == "" {
				groups = append(groups, group{items: []T{it}, chars: n})
				continue
			}
			g, ok := byKey[key]
			if !ok {
				g = &group{key: key}
				byKey[key] = g
				order = append(order, key)
			}
			g.items = append(g.items, it)
			g.chars += n
		}
		for _, key := range order {
			groups = append(groups, *byKey[key])
		}
	}

	// A group larger than maxCandidates can never fit one batch as a whole —
	// chunk it into maxCandidates-sized pieces rather than dissolving it into
	// loose singleton items. Singletons would get bin-packed individually
	// alongside every other group's items, which reintroduces the exact
	// cross-group mixing this grouping exists to avoid (e.g. rerank's
	// same-object pooling: an oversized "营销中心" group splitting into
	// singletons would let its members land in batches full of unrelated
	// objects, right back to the noise this was meant to remove). A chunk
	// stays pure to its group; it just can't offer the same-batch guarantee
	// across chunk boundaries.
	flattened := make([]group, 0, len(groups))
	for _, g := range groups {
		if len(g.items) <= maxCandidates {
			flattened = append(flattened, g)
			continue
		}
		for i := 0; i < len(g.items); i += maxCandidates {
			end := i + maxCandidates
			if end > len(g.items) {
				end = len(g.items)
			}
			chunk := g.items[i:end]
			chars := 0
			for _, it := range chunk {
				itemJSON, _ := json.Marshal(it)
				chars += utf8.RuneCount(itemJSON)
			}
			flattened = append(flattened, group{items: chunk, chars: chars, key: g.key})
		}
	}
	groups = flattened

	sort.SliceStable(groups, func(i, j int) bool { return groups[i].chars > groups[j].chars })

	totalChars := 0
	totalItems := 0
	for _, g := range groups {
		totalChars += g.chars
		totalItems += len(g.items)
	}
	numBatches := (totalChars + maxChars - 1) / maxChars
	if byCount := (totalItems + maxCandidates - 1) / maxCandidates; byCount > numBatches {
		numBatches = byCount
	}
	if numBatches < 1 {
		numBatches = 1
	}
	batches := make([][]T, numBatches)
	batchChars := make([]int, numBatches)
	batchCounts := make([]int, numBatches)
	// batchKey tracks which non-empty group key (if any) has claimed a batch
	// — "" means unclaimed (only keyless items placed so far, or empty).
	// Once a non-empty-key group claims a batch, only more of that same key
	// (or keyless filler) may join it: a different non-empty key is never
	// allowed to share the batch. This is what actually delivers the
	// same-object isolation groupKeyOf exists for — without it, two
	// same-key groups landing in different (correctly isolated) batches
	// could still each get topped up with a *different* other object's
	// candidates as filler, which is exactly the cross-object noise
	// rerankObjectGroupKey was introduced to remove (see its doc comment).
	batchKey := make([]string, numBatches)

	for _, g := range groups {
		best := -1
		for b := 0; b < len(batches); b++ {
			if batchCounts[b]+len(g.items) > maxCandidates {
				continue
			}
			if len(batches[b]) > 0 && batchChars[b]+g.chars > maxChars {
				continue
			}
			if g.key != "" && batchKey[b] != "" && batchKey[b] != g.key {
				continue
			}
			if best == -1 || batchChars[b] < batchChars[best] {
				best = b
			}
		}
		if best == -1 {
			batches = append(batches, nil)
			batchChars = append(batchChars, 0)
			batchCounts = append(batchCounts, 0)
			batchKey = append(batchKey, "")
			best = len(batches) - 1
		}
		batches[best] = append(batches[best], g.items...)
		batchChars[best] += g.chars
		batchCounts[best] += len(g.items)
		if g.key != "" {
			batchKey[best] = g.key
		}
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
func runJudgeBatches[T any](ctx context.Context, items []T, maxChars, maxCandidates, concurrency int, idOf func(T) string, groupKeyOf func(T) string, defaultForMissing string, callBatch func(ctx context.Context, batch []T) (map[string]string, error)) (map[string]string, error) {
	batches := splitJudgeBatches(items, maxChars, maxCandidates, groupKeyOf)
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

// runMatchBatches is the fan-out for LLM steps that report only the matched
// item ids (not a verdict for every input item) — e.g. domain/source
// filtering, where "not mentioned in the response" simply means "not a
// match" rather than a gap to retry or default. Unlike runJudgeBatches, there
// is no full-coverage expectation: callBatch returns the subset of the
// batch's ids the LLM judged a match, and any id it omits is treated as a
// negative result, not a missing one.
func runMatchBatches[T any](ctx context.Context, items []T, maxChars, maxCandidates, concurrency int, callBatch func(ctx context.Context, batch []T) ([]string, error)) (map[string]bool, error) {
	batches := splitJudgeBatches(items, maxChars, maxCandidates, nil)
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
	slog.Info("retrieval: match batches", "batch_count", len(batches), "batch_sizes", batchSizes, "concurrency", concurrency)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	matched := make(map[string]bool)
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
			ids, err := callBatch(ctx, batch)
			batchMs := time.Since(batchStart).Milliseconds()
			if err != nil {
				slog.Info("retrieval: match batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs, "error", err)
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			slog.Info("retrieval: match batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs, "matched", len(ids))
			mu.Lock()
			for _, id := range ids {
				matched[id] = true
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
	return matched, nil
}
