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
// whose KP is still lifecycle=current, plus the subject-dimension
// SynonymResolver (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Cache invalidation is explicit — CreateLink / TransitionLink /
// AppendObservedCondition / unit lifecycle notifier / synonym confirm-reject
// call InvalidateCache; both the link cache and the synonym table reload
// together on the next Match (one loadCache, one DB round trip pair).
type Matcher struct {
	store *Store

	mu       sync.RWMutex
	cache    []ActivationLink
	synonyms *SynonymResolver
	valid    bool
}

func NewMatcher(store *Store) *Matcher {
	return &Matcher{store: store, synonyms: NewSynonymResolver()}
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
	synonyms, err := m.store.ListActiveSynonyms()
	if err != nil {
		return nil, err
	}
	m.synonyms.Load(synonyms)
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

	queryTopic, qi, qa, qc := BuildQueryConditionTerms(query.Subject, query.Intent, query.Audience, query.Constraint, m.synonyms)
	qq := text.Terms(text.Normalize(query.ExpandedQuestion))

	var results []LinkMatch
	for _, link := range links {
		// Question-level shortcut: if this exact literal question has
		// activated this link before (any four-tuple, any observed
		// condition group — migration 047 known_question_terms), match
		// directly. Checked before the four-tuple gate so intent/audience/
		// constraint extraction jitter on a repeat ask of the same question
		// can never miss a link it previously matched, and never spawns a
		// redundant observed_conditions group for what's really the same
		// question (2026-08-09 决策，见对话记录).
		if qq != "" && containsTermString(link.KnownQuestionTerms, qq) {
			results = append(results, LinkMatch{Link: link, Score: 1.0})
			continue
		}

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

		if MatchConditionGroups(conds, queryTopic, qi, qa, qc, m.synonyms) {
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

// containsTermString reports whether terms is present in the (small,
// already-deduped) set. Linear scan is fine — known_question_terms is
// capped at maxKnownQuestionTerms per link and this only runs per-link
// per-query, not against the whole corpus.
func containsTermString(set []string, terms string) bool {
	for _, s := range set {
		if s == terms {
			return true
		}
	}
	return false
}

// BuildQueryConditionTerms normalizes a raw subject/intent/audience/constraint
// four-tuple into the form MatchConditionGroups compares against
// ObservedCondition groups — shared by Match and by Wiki's four-tuple
// retrieval entry (docs/design/wiki-compilation.md "触发问法取材真实观测，
// 检索匹配复用四元组") so both callers use the exact same normalization.
func BuildQueryConditionTerms(subject, intent, audience, constraint string, resolver *SynonymResolver) (queryTopic, qi, qa, qc string) {
	queryTopic = resolver.Canonicalize(strings.TrimSpace(text.Normalize(subject) + " " + text.Normalize(intent)))
	qi = text.Terms(text.Normalize(intent))
	qc = text.Terms(text.Normalize(constraint))
	qa = text.NormalizeCompact(audience)
	return
}

// MatchConditionGroups reports whether any of conds agrees with the query on
// all four dimensions (subject via coreContained after synonym
// canonicalization, others by equality). Exported for Wiki's four-tuple
// retrieval entry, which reuses this instead of a second matching
// implementation.
//
// Guards against an all-empty query: without this, a query with no
// intent/audience/constraint (e.g. the plain POST /answer path that skips
// Session parsing) would trivially equal a condition group that itself has
// empty intent/audience/constraint, producing a false-positive match. Match's
// own zero-groups branch only guards the len(conds)==0 case, not this one, so
// the guard lives here rather than being left to each caller to replicate.
func MatchConditionGroups(conds []ObservedCondition, queryTopic, qi, qa, qc string, resolver *SynonymResolver) bool {
	if queryTopic == "" && qi == "" && qa == "" && qc == "" {
		return false
	}
	for _, cond := range conds {
		core := text.SplitTerms(text.Terms(resolver.Canonicalize(cond.Subject)))
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

// SubjectOnlyMiss reports whether any group in conds has intent/audience/
// constraint all equal to the query's, but subject fails coreContained even
// after synonym canonicalization — the diagnostic Trace's near-miss
// detection uses to mine subject_synonym_gap candidates (docs/impl/v1/trace.md
// 步骤 3, docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Ensures the resolver is warm (loads the cache if Match hasn't run yet in
// this process). Returns the representative group's normalized Subject
// (hit_count-highest among qualifying groups) as observedSubject.
func (m *Matcher) SubjectOnlyMiss(conds []ObservedCondition, subject, intent, audience, constraint string) (observedSubject string, ok bool) {
	if len(conds) == 0 {
		return "", false
	}
	if _, err := m.loadCache(); err != nil {
		return "", false
	}

	queryTopic, qi, qa, qc := BuildQueryConditionTerms(subject, intent, audience, constraint, m.synonyms)
	normalizedSubject := text.Normalize(subject)

	var best ObservedCondition
	found := false
	for _, cond := range conds {
		if qi != cond.Intent || qa != cond.Audience || qc != cond.Constraint {
			continue
		}
		core := text.SplitTerms(text.Terms(m.synonyms.Canonicalize(cond.Subject)))
		fullyMatched := false
		if len(core) == 0 {
			fullyMatched = queryTopic == ""
		} else {
			fullyMatched = queryTopic != "" && coreContained(core, queryTopic)
		}
		if fullyMatched {
			continue // this group already Matches — not a miss
		}
		if cond.Subject == normalizedSubject {
			// Contradiction guard: identical normalized subject failing
			// coreContained shouldn't happen; skip rather than misreport.
			continue
		}
		if !found || cond.HitCount > best.HitCount {
			best = cond
			found = true
		}
	}
	if !found {
		return "", false
	}
	return best.Subject, true
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
