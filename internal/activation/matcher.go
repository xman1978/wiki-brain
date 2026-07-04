package activation

import (
	"sort"
	"sync"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

// Default thresholds (docs/impl/v1/activation.md 步骤 2), overridable via
// config.yml retrieval.activation_match_*.
const (
	DefaultMatchMin         = 0.7
	DefaultMatchMinFallback = 0.85
	DefaultMatchTop         = 5
)

// MatchConfig carries the configurable thresholds in from config.yml
// (retrieval section). Zero values fall back to the documented defaults.
type MatchConfig struct {
	MatchMin         float64
	MatchMinFallback float64
	MatchTop         int
}

func (c MatchConfig) withDefaults() MatchConfig {
	if c.MatchMin <= 0 {
		c.MatchMin = DefaultMatchMin
	}
	if c.MatchMinFallback <= 0 {
		c.MatchMinFallback = DefaultMatchMinFallback
	}
	if c.MatchTop <= 0 {
		c.MatchTop = DefaultMatchTop
	}
	return c
}

// Matcher is the pure-program activation condition matcher — no LLM calls
// (docs/impl/v1/activation.md 步骤 2). It caches every verified link whose
// target KP is lifecycle=current in memory; the cache is invalidated
// whenever activation_links or KP lifecycle changes (two call sites: this
// package's own TransitionLink, and unit.Service's ActivationNotifier hook).
type Matcher struct {
	store *Store

	mu     sync.RWMutex
	cache  []ActivationLink
	loaded bool
}

func NewMatcher(store *Store) *Matcher {
	return &Matcher{store: store}
}

func (m *Matcher) InvalidateCache() {
	m.mu.Lock()
	m.loaded = false
	m.cache = nil
	m.mu.Unlock()
}

func (m *Matcher) loadCache() ([]ActivationLink, error) {
	m.mu.RLock()
	if m.loaded {
		cache := m.cache
		m.mu.RUnlock()
		return cache, nil
	}
	m.mu.RUnlock()

	links, err := m.store.ListVerifiedLinksForCurrentKP()
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cache = links
	m.loaded = true
	m.mu.Unlock()

	return links, nil
}

// Match implements docs/impl/v1/activation.md 步骤 2 in full: hard gating on
// audience/constraint, then subject/intent scoring for links with a subject
// condition, falling back to question_terms overlap (higher threshold, no
// gated links) for links or queries missing the quadruple.
func (m *Matcher) Match(query session.ExpandedQuery, cfg MatchConfig) ([]LinkMatch, error) {
	cfg = cfg.withDefaults()

	links, err := m.loadCache()
	if err != nil {
		return nil, err
	}

	qs := text.TermSet(query.Subject)
	qi := text.TermSet(query.Intent)
	qc := text.TermSet(query.Constraint)
	qa := text.NormalizeCompact(query.Audience)
	qq := text.TermSet(query.ExpandedQuestion)

	var results []LinkMatch
	for _, link := range links {
		if link.SubjectTerms == "" || len(qs) == 0 {
			if link.Audience != "" || link.ConstraintTerms != "" {
				continue
			}
			lq := text.SplitTerms(link.QuestionTerms)
			score := overlapRatio(qq, lq)
			if score >= cfg.MatchMinFallback {
				results = append(results, LinkMatch{Link: link, Score: score})
			}
			continue
		}

		if !audienceGate(link.Audience, qa) {
			continue
		}
		if !constraintGate(link.ConstraintTerms, qc) {
			continue
		}

		ls := text.SplitTerms(link.SubjectTerms)
		li := text.SplitTerms(link.IntentTerms)

		sSubject := overlapRatio(qs, ls)
		sIntent := 1.0
		if len(li) > 0 {
			sIntent = overlapRatio(qi, li)
		}
		score := 0.7*sSubject + 0.3*sIntent
		if score >= cfg.MatchMin {
			results = append(results, LinkMatch{Link: link, Score: score})
		}
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > cfg.MatchTop {
		results = results[:cfg.MatchTop]
	}
	return results, nil
}

// overlapRatio uses the link side as the denominator: link conditions come
// from historical normalized questions and are typically shorter than a new
// question, so a plain Jaccard ratio would systematically under-score
// (docs/impl/v1/activation.md 步骤 2, closing note).
func overlapRatio(query, link map[string]struct{}) float64 {
	if len(link) == 0 {
		return 0
	}
	hits := 0
	for w := range query {
		if _, ok := link[w]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(link))
}

func audienceGate(linkAudience, queryAudience string) bool {
	if linkAudience == "" {
		return true
	}
	if queryAudience == "" {
		return false
	}
	return linkAudience == queryAudience
}

// constraintGate requires the link's constraint terms to be fully covered by
// the query's — the link's scoping must hold, but the query is allowed to
// carry additional constraints the link doesn't know about
// (docs/impl/v1/activation.md 步骤 2, "守门方向不对称").
func constraintGate(linkConstraintTerms string, queryConstraint map[string]struct{}) bool {
	if linkConstraintTerms == "" {
		return true
	}
	if len(queryConstraint) == 0 {
		return false
	}
	linkSet := text.SplitTerms(linkConstraintTerms)
	for w := range linkSet {
		if _, ok := queryConstraint[w]; !ok {
			return false
		}
	}
	return true
}
