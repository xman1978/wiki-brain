package activation

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/session"
)

// MatchConfig holds the knobs Match reads from retrieval config
// (docs/impl/v1/retrieval.md). Zero values are replaced with defaults in Match.
type MatchConfig struct {
	MatchTop int
}

func (c MatchConfig) withDefaults() MatchConfig {
	if c.MatchTop <= 0 {
		c.MatchTop = 5
	}
	return c
}

// Matcher holds an in-memory cache of matchable (verified+candidate) links
// whose KP is still lifecycle=current. Cache invalidation is explicit —
// CreateLink / TransitionLink / AppendObservedCondition / unit lifecycle
// notifier call InvalidateCache.
type Matcher struct {
	store *Store

	mu    sync.RWMutex
	cache []ActivationLink
	valid bool
}

func NewMatcher(store *Store) *Matcher {
	return &Matcher{store: store}
}

func (m *Matcher) InvalidateCache() {
	m.mu.Lock()
	m.valid = false
	m.cache = nil
	m.mu.Unlock()
}

func (m *Matcher) loadCache() ([]ActivationLink, error) {
	m.mu.RLock()
	if m.valid {
		out := m.cache
		m.mu.RUnlock()
		return out, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.valid {
		return m.cache, nil
	}
	links, err := m.store.ListMatchableLinksForCurrentKP()
	if err != nil {
		return nil, err
	}
	m.cache = links
	m.valid = true
	return links, nil
}

// Match scores activation links against the Session ExpandedQuery using
// observed condition groups: within a group all four fields must agree;
// across groups any hit activates the link (OR). See
// docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md.
//
// Empty observed_conditions falls back to question_terms equality only when
// the link has never observed a non-empty audience/constraint gate.
func (m *Matcher) Match(query session.ExpandedQuery, cfg MatchConfig) ([]LinkMatch, error) {
	cfg = cfg.withDefaults()

	links, err := m.loadCache()
	if err != nil {
		return nil, err
	}

	queryTopic := strings.TrimSpace(text.Normalize(query.Subject) + " " + text.Normalize(query.Intent))
	qi := text.Terms(text.Normalize(query.Intent))
	qc := text.Terms(text.Normalize(query.Constraint))
	qa := text.NormalizeCompact(query.Audience)
	qq := text.Terms(text.Normalize(query.ExpandedQuestion))

	var results []LinkMatch
	for _, link := range links {
		conds := link.ObservedConditions
		if len(conds) == 0 {
			if HasNonEmptyGate(conds) || hasNonEmpty(link.Audience) || hasNonEmpty(link.ConstraintTerms) {
				continue
			}
			if qq != "" && qq == link.QuestionTerms {
				results = append(results, LinkMatch{Link: link, Score: 1.0})
			}
			continue
		}

		if conditionGroupMatches(conds, queryTopic, qi, qa, qc) {
			results = append(results, LinkMatch{Link: link, Score: 1.0})
		}
	}

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

func conditionGroupMatches(conds []ObservedCondition, queryTopic, qi, qa, qc string) bool {
	for _, cond := range conds {
		core := text.SplitTerms(text.Terms(cond.Subject))
		if len(core) == 0 {
			if queryTopic != "" {
				continue
			}
		} else if queryTopic == "" || !coreContained(core, queryTopic) {
			continue
		}
		if qi != cond.Intent {
			continue
		}
		if qa != cond.Audience {
			continue
		}
		if qc != cond.Constraint {
			continue
		}
		return true
	}
	return false
}

func coreContained(core map[string]struct{}, topicText string) bool {
	for w := range core {
		if !strings.Contains(topicText, w) {
			return false
		}
	}
	return true
}

func hasNonEmpty(set []string) bool {
	for _, s := range set {
		if s != "" {
			return true
		}
	}
	return false
}
