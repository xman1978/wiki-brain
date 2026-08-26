package activation

import (
	"context"
	"math/rand"
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
// whose KP is still lifecycle=current. Match() compares all four fields by
// exact equality (2026-08-12 修订，见下方 BuildQueryConditionTerms/
// MatchConditionGroups) — it does not consult subject_synonyms at all; that
// table's other consumers (Wiki concept page alias display, preset data
// import) query it directly via SQL, independent of this cache.
// Cache invalidation is explicit — CreateLink / TransitionLink /
// AppendObservedCondition / unit lifecycle notifier call InvalidateCache.
type Matcher struct {
	store *Store

	// mu guards cache (2026-08-25, domain-sharded cache): cache is keyed by
	// domainCacheKey(domainIDs) so each distinct domain combination Match()
	// sees loads and invalidates independently, instead of one global
	// scan-everything blob — a query scoped to one domain no longer pays for
	// every other domain's links. InvalidateCache clears the whole map: any
	// write can affect any shard's candidate set, and shard keys are cheap to
	// rebuild lazily.
	mu    sync.RWMutex
	cache map[string][]ActivationLink

	// confidenceCfg / randFloat drive the 2026-08-13 tiering + explore/audit
	// sampling (docs/impl/v1/activation.md「置信度分档判定」). randFloat
	// defaults to rand.Float64 and is injectable so tests can assert sampling
	// boundaries deterministically (fixed 0.0/1.0 stubs).
	confidenceCfg ConfidenceConfig
	randFloat     func() float64
}

func NewMatcher(store *Store) *Matcher {
	return &Matcher{store: store, randFloat: rand.Float64}
}

// SetConfidenceConfig wires the retrieval.* confidence knobs
// (docs/impl/v1/activation.md 配置项). Zero-value ConfidenceConfig makes
// every condition tier as exploring with 0 explore rate — callers (main.go)
// must call this before Match sees production traffic.
func (m *Matcher) SetConfidenceConfig(cfg ConfidenceConfig) {
	m.mu.Lock()
	m.confidenceCfg = cfg
	m.mu.Unlock()
}

func (m *Matcher) InvalidateCache() {
	m.mu.Lock()
	m.cache = nil
	m.mu.Unlock()
}

// domainCacheKey derives a stable map key for a domain-scoped cache shard —
// order-independent (sorted) so callers passing the same domain set in a
// different order share a shard. Empty domainIDs (unresolved domain) maps to
// its own reserved key, distinct from any real domain_id.
func domainCacheKey(domainIDs []string) string {
	if len(domainIDs) == 0 {
		return "\x00all"
	}
	sorted := append([]string(nil), domainIDs...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x1f")
}

func (m *Matcher) loadCache(domainIDs []string) ([]ActivationLink, error) {
	key := domainCacheKey(domainIDs)

	m.mu.RLock()
	if links, ok := m.cache[key]; ok {
		m.mu.RUnlock()
		return links, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if links, ok := m.cache[key]; ok {
		return links, nil
	}
	links, err := m.store.ListMatchableLinksForCurrentKP(domainIDs)
	if err != nil {
		return nil, err
	}
	if m.cache == nil {
		m.cache = make(map[string][]ActivationLink)
	}
	m.cache[key] = links
	return links, nil
}

// Match scores activation links against the Session ExpandedQuery using
// observed condition groups: within a group all four fields must agree;
// across groups any hit activates the link (OR). See
// docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md.
//
// Empty observed_conditions falls back to question_terms equality only when
// the link has never observed a non-empty audience/constraint gate.
//
// domainIDs scopes the candidate scan to those domains (2026-08-25,
// domain-sharded cache) — pass nil/empty when the query's domain is
// unresolved to scan every domain, same as before this scoping was added.
func (m *Matcher) Match(_ context.Context, query session.ExpandedQuery, domainIDs []string, cfg MatchConfig) ([]LinkMatch, error) {
	cfg = cfg.withDefaults()

	links, err := m.loadCache(domainIDs)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	confCfg := m.confidenceCfg
	randFloat := m.randFloat
	m.mu.RUnlock()

	querySubject, qi, qa, qc := BuildQueryConditionTerms(query.Subject, query.Intent, query.Audience, query.Constraint)
	qq := text.Terms(text.Normalize(query.ExpandedQuestion))

	var results []LinkMatch
	for _, link := range links {
		conds := link.ObservedConditions

		// Question-level shortcut (2026-08-13 改判，见 docs/impl/v1/
		// activation.md「字面问题捷径与置信度档位」): find the owning
		// condition (known_question_terms now lives per-condition), then
		// route through the same tiering as an ordinary match — this is no
		// longer an unconditional bypass.
		if qq != "" {
			if owner, ok := findOwningCondition(conds, qq); ok {
				if m, matched := tierAndAppend(link, owner, confCfg, randFloat); matched {
					results = append(results, m)
				}
				continue
			}
		}

		if len(conds) == 0 {
			if HasNonEmptyGate(conds) || hasNonEmpty(link.Audience) || hasNonEmpty(link.ConstraintTerms) {
				continue
			}
			if qq != "" && qq == link.QuestionTerms {
				results = append(results, LinkMatch{Link: link, Score: 1.0, MatchedBy: MatchedByExact})
			}
			continue
		}

		if owner, ok := findMatchingCondition(conds, querySubject, qi, qa, qc); ok {
			if m, matched := tierAndAppend(link, owner, confCfg, randFloat); matched {
				results = append(results, m)
			}
		}
	}

	sortByMatchedByThenRecency(results)
	if len(results) > cfg.MatchTop {
		results = results[:cfg.MatchTop]
	}
	return results, nil
}

// findOwningCondition scans conds for the one whose KnownQuestionTerms
// contains the normalized literal question qq (docs/impl/v1/activation.md
// 「字面问题捷径」owning-condition lookup).
func findOwningCondition(conds []ObservedCondition, qq string) (ObservedCondition, bool) {
	for _, c := range conds {
		if containsTermString(c.KnownQuestionTerms, qq) {
			return c, true
		}
	}
	return ObservedCondition{}, false
}

// findMatchingCondition returns the first condition group that exactly
// agrees with the query on all four dimensions (same semantics as
// MatchConditionGroups, but returns the condition itself rather than a bool
// so the caller can tier it).
func findMatchingCondition(conds []ObservedCondition, querySubject, qi, qa, qc string) (ObservedCondition, bool) {
	if querySubject == "" && qi == "" && qa == "" && qc == "" {
		return ObservedCondition{}, false
	}
	for _, cond := range conds {
		if cond.Subject != querySubject {
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
		return cond, true
	}
	return ObservedCondition{}, false
}

// tierAndAppend implements docs/impl/v1/activation.md「置信度分档判定」for a
// single matched condition: exploring only serves with probability
// explore_rate_low (a miss is treated as no-hit this round, matching the
// doc's explicit "试探未中选，等价于本轮当作未命中处理"); self_graded/trusted
// always serve, with an independent Bernoulli draw for AuditSampled.
func tierAndAppend(link ActivationLink, cond ObservedCondition, cfg ConfidenceConfig, randFloat func() float64) (LinkMatch, bool) {
	tier, mean := conditionTier(cond, cfg)
	base := LinkMatch{
		Link: link, Score: 1.0, MatchedBy: MatchedByExact,
		Tier: tier, Mean: mean,
		Subject: cond.Subject, Intent: cond.Intent, Audience: cond.Audience, Constraint: cond.Constraint,
	}
	switch tier {
	case TierExploring:
		if randFloat() < cfg.ExploreRateLow {
			return base, true
		}
		return LinkMatch{}, false
	case TierTrusted:
		base.AuditSampled = randFloat() < cfg.ExploreRateTrusted
		return base, true
	default: // self_graded
		base.AuditSampled = randFloat() < cfg.ExploreRateSelfGraded
		return base, true
	}
}

// sortByMatchedByThenRecency puts exact matches before model-assisted ones,
// and within each tier orders by LastUsedAt descending — the tie-break both
// Match (over ActivationLink) and bundleMatchCore (over ActivationBundle)
// share.
func sortByMatchedByThenRecency(results []LinkMatch) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].MatchedBy != results[j].MatchedBy {
			return results[i].MatchedBy == MatchedByExact
		}
		var ti, tj time.Time
		if results[i].Link.LastUsedAt.Valid {
			ti = results[i].Link.LastUsedAt.Time
		}
		if results[j].Link.LastUsedAt.Valid {
			tj = results[j].Link.LastUsedAt.Time
		}
		return ti.After(tj)
	})
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
//
// 2026-08-12 修订：subject 不再经同义词归一化/包含判断，四个维度统一改为
// 精确相等（同 intent/audience/constraint）——理由见 CLAUDE.md 对应决策：
// 四元组四个字段在真实抽取中都存在措辞抖动，只对 subject 做模糊匹配、其余
// 三项硬性精确匹配这套不对称设计站不住脚；且 round 2 模型辅助判断（原本
// 专门覆盖"subject 同义词归一化后仍不中"这一种情况）随之一并移除。
// subject_synonyms 表、Wiki 概念页别名展示、预置数据导入均不受影响，继续
// 独立运作（2026-08-26：gap-mining 挖掘链路 SubjectOnlyMiss 已删除——
// TupleNormalizer 上线后，其触发窗口在真实流量下已被结构性清空，详见
// docs/impl/v1/activation.md）。
func BuildQueryConditionTerms(subject, intent, audience, constraint string) (querySubject, qi, qa, qc string) {
	querySubject = text.Normalize(subject)
	qi = text.Terms(text.Normalize(intent))
	qc = text.Terms(text.Normalize(constraint))
	qa = text.NormalizeCompact(audience)
	return
}

// MatchConditionGroups reports whether any of conds agrees with the query on
// all four dimensions, all by exact equality (2026-08-12 修订，见
// BuildQueryConditionTerms). Exported for Wiki's four-tuple retrieval entry,
// which reuses this instead of a second matching implementation.
//
// Guards against an all-empty query: without this, a query with no
// intent/audience/constraint (e.g. the plain POST /answer path that skips
// Session parsing) would trivially equal a condition group that itself has
// empty intent/audience/constraint, producing a false-positive match. Match's
// own zero-groups branch only guards the len(conds)==0 case, not this one, so
// the guard lives here rather than being left to each caller to replicate.
func MatchConditionGroups(conds []ObservedCondition, querySubject, qi, qa, qc string) bool {
	if querySubject == "" && qi == "" && qa == "" && qc == "" {
		return false
	}
	for _, cond := range conds {
		if cond.Subject != querySubject {
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

func hasNonEmpty(set []string) bool {
	for _, s := range set {
		if s != "" {
			return true
		}
	}
	return false
}
