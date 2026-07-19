package activation

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

// Default result cap (docs/impl/v1/activation.md 步骤 2), overridable via
// config.yml retrieval.activation_match_top. Matching itself is exact —
// there is no score threshold to configure.
const DefaultMatchTop = 5

// MatchConfig carries the configurable result cap in from config.yml
// (retrieval section). Zero value falls back to the documented default.
type MatchConfig struct {
	MatchTop int
}

func (c MatchConfig) withDefaults() MatchConfig {
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

// Match implements docs/impl/v1/activation.md 步骤 2: the activation link is
// a precise-hit cache, not a small retrieval engine, so a false positive here
// answers the user directly with no downstream check, while a false negative
// only costs a fallback to the slow path (which already works). Every hit
// scores 1.0; there is no partial credit.
//
// subject_terms is matched by substring containment against the query's
// subject+intent combined text, not token-set equality or even token-set
// subset. Two real-world gaps found by testing against live traffic drove
// this (both confirmed via actual queries against production-shaped data,
// not hypothetical):
//
//  1. Session doesn't reliably put the same concept in the same slot across
//     rephrasings of the same question — "扣分" landed in `subject` for the
//     historical trace that put it into a link's stored core, but in
//     `intent` for a fresh, on-topic rephrasing. A subset check against
//     subject alone misses this even though the concept is right there in
//     the query. Checking containment against subject+intent combined fixes
//     it without having to also broaden what Study is willing to put in the
//     core in the first place (computeLinkCondition/LabelTermIntersection
//     are untouched — still subject-only, still conservative).
//  2. The gse segmenter doesn't draw the same word boundary for the same
//     compound noun on every call — "数据库连接" as one token in the trace
//     that built the core, "数据库"+"连接" as two tokens in a fresh query
//     containing the identical substring. Token-set membership treats that
//     as two different words; substring containment doesn't care where the
//     tokenizer drew the boundary.
//
// This trades a small amount of precision (a short core word could in
// principle match inside an unrelated longer word) for recall on exactly
// the two failure modes above; audience/constraint stay exact-match hard
// gates below specifically to bound that risk — a topic-word coincidence
// alone can never activate a link whose audience/constraint don't also
// agree.
//
// intent_terms/audience/constraint_terms are matched by set membership:
// Study accumulates every distinct normalized value it has independently
// confirmed for this point (including "" when a confident trace's field
// came back blank — that is itself a real observation, not a gap to
// special-case) into a whitelist that only grows, so a hit requires the
// query's (single) normalized value to be *in* that set.
func (m *Matcher) Match(query session.ExpandedQuery, cfg MatchConfig) ([]LinkMatch, error) {
	cfg = cfg.withDefaults()

	links, err := m.loadCache()
	if err != nil {
		return nil, err
	}

	// queryTopic combines subject+intent into one text blob for the core
	// containment check (see doc comment above) — a space between them keeps
	// a word at the end of one from fusing with a word at the start of the
	// other into something neither actually said.
	queryTopic := strings.TrimSpace(text.Normalize(query.Subject) + " " + text.Normalize(query.Intent))
	qi := text.Terms(text.Normalize(query.Intent))
	qc := text.Terms(text.Normalize(query.Constraint))
	qa := text.NormalizeCompact(query.Audience)
	qq := text.Terms(text.Normalize(query.ExpandedQuestion))

	var results []LinkMatch
	for _, link := range links {
		linkCore := text.SplitTerms(link.SubjectTerms)
		if len(linkCore) == 0 || queryTopic == "" {
			// Fallback: no reliable subject signal on either side (stale
			// link predating the quadruple, or this turn's Session parse
			// degraded). audience/constraint can't be verified for equality
			// with confidence here, so only links that have never observed a
			// real (non-empty) restriction on either field are eligible — a
			// gated link must never be reachable through this branch. A set
			// that's empty or contains only "" both count as "never gated";
			// only a set holding an actual value counts as gated.
			if hasNonEmpty(link.Audience) || hasNonEmpty(link.ConstraintTerms) {
				continue
			}
			if qq != "" && qq == link.QuestionTerms {
				results = append(results, LinkMatch{Link: link, Score: 1.0})
			}
			continue
		}

		if !coreContained(linkCore, queryTopic) {
			continue
		}
		if !containsString(link.IntentTerms, qi) {
			continue
		}
		if !containsString(link.Audience, qa) {
			continue
		}
		if !containsString(link.ConstraintTerms, qc) {
			continue
		}
		results = append(results, LinkMatch{Link: link, Score: 1.0})
	}

	// Exact matches carry no score to rank by; break ties by recency so the
	// most recently useful link wins when a cap trims the result set.
	sort.SliceStable(results, func(i, j int) bool {
		var ti, tj time.Time
		if results[i].Link.LastUsedAt.Valid {
			ti = results[i].Link.LastUsedAt.Time
		}
		if results[j].Link.LastUsedAt.Valid {
			tj = results[j].Link.LastUsedAt.Time
		}
		return ti.After(tj)
	})
	if len(results) > cfg.MatchTop {
		results = results[:cfg.MatchTop]
	}
	return results, nil
}

// coreContained reports whether every word in core appears as a substring of
// topicText — deliberately substring, not token-set membership (see Match's
// doc comment for why): the gse segmenter doesn't always draw the same word
// boundary for the same compound noun across calls, so requiring the query's
// own tokenization to reproduce the exact boundary the core word was stored
// with is needlessly fragile.
func coreContained(core map[string]struct{}, topicText string) bool {
	for w := range core {
		if !strings.Contains(topicText, w) {
			return false
		}
	}
	return true
}

// containsString reports whether v is a member of set — the whitelist
// membership test for intent_terms/audience/constraint_terms. "" is a
// legitimate member like any other value (see Match's doc comment). An
// empty set (this field was never populated — a link predating condition
// sets, or one built without touching it) is treated the same as a set
// containing only "": both mean "no value has been confirmed for this
// field", so an empty query value still matches (generalizing the original
// scalar rule that "" == "" counted as agreement) while a non-empty query
// value correctly does not.
func containsString(set []string, v string) bool {
	if v == "" && len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// hasNonEmpty reports whether set contains at least one non-empty value —
// used only to gate the fallback branch, where the question is "has this
// link ever observed a real restriction" rather than plain set membership.
func hasNonEmpty(set []string) bool {
	for _, s := range set {
		if s != "" {
			return true
		}
	}
	return false
}
