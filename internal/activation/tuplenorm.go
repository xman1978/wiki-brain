package activation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// TupleNormConfig mirrors MatchConfig's pattern: the small subset of
// RetrievalConfig's question-tuple-normalization knobs TupleNormalizer
// actually reads (docs/impl/v1/retrieval.md 步骤 2).
type TupleNormConfig struct {
	LocalSimMin        float64
	VectorMatchEnabled bool
	VectorSimMin       float64
}

func (c TupleNormConfig) withDefaults() TupleNormConfig {
	if c.LocalSimMin <= 0 {
		c.LocalSimMin = 0.8
	}
	if c.VectorSimMin <= 0 {
		c.VectorSimMin = 0.75
	}
	return c
}

// TupleNormalizer implements the four-tier normalization pass that runs
// before Retrieval feeds a Session four-tuple into Matcher/BundleMatcher/
// Wiki's matchFourTupleEntry (all three are now plain exact-match, see
// matcher.go). LLM extraction jitter means the same real question can
// extract to slightly different subject/intent/audience/constraint text
// each time; Normalize replaces a jittered extraction with the first-seen
// canonical tuple for the same underlying question when it can establish
// that with enough confidence, tier by tier, cheapest first.
type TupleNormalizer struct {
	store     *Store
	llmClient llm.LLMClient
	embedder  VectorEmbedder
	cfg       TupleNormConfig
}

func NewTupleNormalizer(store *Store, cfg TupleNormConfig) *TupleNormalizer {
	return &TupleNormalizer{store: store, cfg: cfg.withDefaults()}
}

func (n *TupleNormalizer) SetLLMClient(c llm.LLMClient) {
	n.llmClient = c
}

func (n *TupleNormalizer) SetEmbedder(e VectorEmbedder) {
	n.embedder = e
}

// Normalize runs the four tiers in order and returns as soon as one hits.
// domainIDs empty ⇒ no tiers can run (nothing to scope the lookup to);
// callers should skip calling Normalize entirely in that case (mirrors the
// qc.DomainResolved guard in Retrieval's tryFastPath) but Normalize itself
// also degrades safely by falling through to tier 5 (new record — a no-op
// since Insert also requires domainIDs).
func (n *TupleNormalizer) Normalize(ctx context.Context, domainIDs []string, subject, intent, audience, constraint string) (normSubject, normIntent, normAudience, normConstraint string, err error) {
	qSubject := text.Normalize(subject)
	qIntent := text.Terms(text.Normalize(intent))
	qAudience := text.NormalizeCompact(audience)
	qConstraint := text.Terms(text.Normalize(constraint))

	if len(domainIDs) == 0 {
		return qSubject, qIntent, qAudience, qConstraint, nil
	}

	// Tier 1: exact match.
	exact, err := n.store.FindExactMatch(domainIDs, qSubject, qIntent, qAudience, qConstraint)
	if err != nil {
		return "", "", "", "", fmt.Errorf("activation: tuple norm tier1: %w", err)
	}
	if exact != nil {
		if err := n.store.TouchLastHit(exact.NormID); err != nil {
			slog.Warn("activation: tuple norm touch last hit failed", "norm_id", exact.NormID, "error", err)
		}
		return exact.Subject, exact.Intent, exact.Audience, exact.ConstraintText, nil
	}

	candidates, err := n.store.ListCandidatesByDomain(domainIDs, 200)
	if err != nil {
		return "", "", "", "", fmt.Errorf("activation: tuple norm list candidates: %w", err)
	}

	// Tier 2: local token-Jaccard similarity, computed per-field then
	// averaged across the four fields (documented choice — per-field
	// averaging keeps a strong match on three fields and a weak match on the
	// fourth from silently passing, unlike concatenating all four into one
	// bag of tokens).
	if len(candidates) > 0 {
		bestIdx := -1
		bestScore := 0.0
		for i, c := range candidates {
			score := tupleJaccard(qSubject, qIntent, qAudience, qConstraint, c.Subject, c.Intent, c.Audience, c.ConstraintText)
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx >= 0 && bestScore >= n.cfg.LocalSimMin {
			match := candidates[bestIdx]
			if err := n.store.TouchLastHit(match.NormID); err != nil {
				slog.Warn("activation: tuple norm touch last hit failed", "norm_id", match.NormID, "error", err)
			}
			return match.Subject, match.Intent, match.Audience, match.ConstraintText, nil
		}
	}

	// Tier 2.5: vector early-reject. Only ever narrows the LLM candidate
	// pool by rejecting outright — never confirms a match on its own
	// (asymmetric design, docs/impl/v1/retrieval.md 步骤 2). Vector
	// similarity below VectorSimMin ⇒ treat as no match at all, skip LLM.
	llmCandidates := candidates
	if n.cfg.VectorMatchEnabled && n.embedder != nil && len(candidates) > 0 {
		queryVec, err := n.embedder.Embed(tupleEmbedText(qSubject, qIntent, qAudience, qConstraint))
		if err != nil {
			slog.Warn("activation: tuple norm embed query failed, skipping vector tier", "error", err)
		} else {
			bestSim := 0.0
			for _, c := range candidates {
				candVec, err := n.embedder.Embed(tupleEmbedText(c.Subject, c.Intent, c.Audience, c.ConstraintText))
				if err != nil {
					continue
				}
				if sim := cosineSimilarity(queryVec, candVec); sim > bestSim {
					bestSim = sim
				}
			}
			if bestSim < n.cfg.VectorSimMin {
				// Clearly not a match — record as a new canonical tuple,
				// never reaching the LLM tier.
				return n.insertNew(domainIDs, qSubject, qIntent, qAudience, qConstraint)
			}
			// >= threshold: proceed to LLM with the full candidate set —
			// vector never single-handedly confirms a match.
		}
	}

	// Tier 3: LLM batch judgment over the (possibly vector-surviving)
	// candidate set.
	if n.llmClient != nil && len(llmCandidates) > 0 {
		matched, idx, err := n.judgeLLM(ctx, qSubject, qIntent, qAudience, qConstraint, llmCandidates)
		if err != nil {
			slog.Warn("activation: tuple norm LLM tier failed, falling through to new record", "error", err)
		} else if matched && idx >= 0 && idx < len(llmCandidates) {
			match := llmCandidates[idx]
			if err := n.store.TouchLastHit(match.NormID); err != nil {
				slog.Warn("activation: tuple norm touch last hit failed", "norm_id", match.NormID, "error", err)
			}
			return match.Subject, match.Intent, match.Audience, match.ConstraintText, nil
		}
	}

	// Tier 4: no tier matched — this becomes the new canonical tuple.
	return n.insertNew(domainIDs, qSubject, qIntent, qAudience, qConstraint)
}

func (n *TupleNormalizer) insertNew(domainIDs []string, subject, intent, audience, constraint string) (string, string, string, string, error) {
	now := time.Now().UTC()
	for _, d := range domainIDs {
		norm := &QuestionTupleNorm{
			DomainID:       d,
			Subject:        subject,
			Intent:         intent,
			Audience:       audience,
			ConstraintText: constraint,
			LastHitAt:      now,
			CreatedAt:      now,
		}
		if err := n.store.InsertTupleNorm(norm); err != nil {
			return "", "", "", "", fmt.Errorf("activation: tuple norm insert new record: %w", err)
		}
	}
	return subject, intent, audience, constraint, nil
}

// tupleJaccard averages per-field token-Jaccard similarity
// (|A∩B|/|A∪B|) across the four fields.
func tupleJaccard(qs, qi, qa, qc, cs, ci, ca, cc string) float64 {
	sum := jaccard(qs, cs) + jaccard(qi, ci) + jaccard(qa, ca) + jaccard(qc, cc)
	return sum / 4
}

// jaccard tokenizes a/b via text.TermSet (Normalize+Terms+SplitTerms) before
// comparing — idempotent for fields already run through Terms (intent/
// constraint) and gives subject/audience (Normalize/NormalizeCompact output,
// not yet tokenized) real per-token comparison instead of one opaque blob.
func jaccard(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	setA := text.TermSet(a)
	setB := text.TermSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1
	}
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	inter := 0
	for w := range setA {
		if _, ok := setB[w]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// tupleEmbedText concatenates the four fields into one embedding input,
// separated by " | " so the embedding model still sees field boundaries.
func tupleEmbedText(subject, intent, audience, constraint string) string {
	return subject + " | " + intent + " | " + audience + " | " + constraint
}

type tupleNormLLMCandidate struct {
	Index    int    `json:"index"`
	Subject  string `json:"subject"`
	Intent   string `json:"intent"`
	Audience string `json:"audience"`
	// Constraint mirrors ObservedCondition's JSON field naming (see
	// conditions.go) even though the store column is constraint_text.
	Constraint string `json:"constraint"`
}

type tupleNormLLMQuery struct {
	Subject    string `json:"subject"`
	Intent     string `json:"intent"`
	Audience   string `json:"audience"`
	Constraint string `json:"constraint"`
}

type tupleNormLLMResult struct {
	Matched        bool `json:"matched"`
	CandidateIndex int  `json:"candidate_index"`
}

func (n *TupleNormalizer) judgeLLM(ctx context.Context, subject, intent, audience, constraint string, candidates []QuestionTupleNorm) (bool, int, error) {
	queryJSON, err := json.Marshal(tupleNormLLMQuery{Subject: subject, Intent: intent, Audience: audience, Constraint: constraint})
	if err != nil {
		return false, -1, fmt.Errorf("marshal query tuple: %w", err)
	}

	cands := make([]tupleNormLLMCandidate, len(candidates))
	for i, c := range candidates {
		cands[i] = tupleNormLLMCandidate{Index: i, Subject: c.Subject, Intent: c.Intent, Audience: c.Audience, Constraint: c.ConstraintText}
	}
	candsJSON, err := json.Marshal(cands)
	if err != nil {
		return false, -1, fmt.Errorf("marshal candidates: %w", err)
	}

	vars := map[string]string{
		"query_tuple": string(queryJSON),
		"candidates":  string(candsJSON),
	}
	raw, err := n.llmClient.CompleteJSON(ctx, "tuple_norm_match.md", vars, "tuple_norm_match")
	if err != nil {
		return false, -1, fmt.Errorf("tuple_norm_match completion: %w", err)
	}

	var result tupleNormLLMResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, -1, fmt.Errorf("unmarshal tuple_norm_match result: %w", err)
	}
	if !result.Matched {
		return false, -1, nil
	}
	return true, result.CandidateIndex, nil
}
