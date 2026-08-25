package activation

import (
	"context"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/jxman78/wiki-brain/internal/session"
)

// sortBundleMatchesByMatchedByThenRecency mirrors sortByMatchedByThenRecency's
// tie-break (exact before model, then LastUsedAt descending) over BundleMatch.
func sortBundleMatchesByMatchedByThenRecency(results []BundleMatch) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].MatchedBy != results[j].MatchedBy {
			return results[i].MatchedBy == MatchedByExact
		}
		var ti, tj time.Time
		if results[i].Bundle.LastUsedAt.Valid {
			ti = results[i].Bundle.LastUsedAt.Time
		}
		if results[j].Bundle.LastUsedAt.Valid {
			tj = results[j].Bundle.LastUsedAt.Time
		}
		return ti.After(tj)
	})
}

// BundleMatcher runs the same match algorithm as Matcher（阶段 1 的硬性过滤 +
// 精确匹配，docs/impl/v1/activation-bundle.md 步骤 2/4）over
// ActivationBundle's ObservedConditions instead of ActivationLink's — this is
// the "Bundle 阶段 1 复用阶段 1 抽出的共享匹配核心" point from the execution
// plan: BuildQueryConditionTerms/MatchConditionGroups/
// sortByMatchedByThenRecency are the shared core; only the per-candidate
// storage shape (ActivationLink vs ActivationBundle) differs, so this file
// re-runs the same loop shape against ActivationBundle rather than forcing a
// generic type over the two.
//
// 2026-08-12 修订：不再持有 SynonymResolver/llmClient——四元组比较改为全字段
// 精确匹配（见 matcher.go BuildQueryConditionTerms 的修订说明），没有 round 2
// 模型辅助判断这一级了。
type BundleMatcher struct {
	store *Store

	// cache is keyed by domainCacheKey(domainIDs) — same domain-sharded cache
	// design as Matcher (2026-08-25), so a query scoped to one domain doesn't
	// pay to scan every domain's bundles.
	mu    sync.RWMutex
	cache map[string][]ActivationBundle

	// confidenceCfg / randFloat mirror Matcher's — same shared tiering core
	// (docs/impl/v1/activation.md「置信度分档判定」), applied over
	// ActivationBundle's trigger-axis ObservedConditions.
	confidenceCfg ConfidenceConfig
	randFloat     func() float64
}

func NewBundleMatcher(store *Store) *BundleMatcher {
	return &BundleMatcher{store: store, randFloat: rand.Float64}
}

// SetConfidenceConfig mirrors Matcher.SetConfidenceConfig.
func (m *BundleMatcher) SetConfidenceConfig(cfg ConfidenceConfig) {
	m.mu.Lock()
	m.confidenceCfg = cfg
	m.mu.Unlock()
}

func (m *BundleMatcher) InvalidateCache() {
	m.mu.Lock()
	m.cache = nil
	m.mu.Unlock()
}

func (m *BundleMatcher) loadCache(domainIDs []string) ([]ActivationBundle, error) {
	key := domainCacheKey(domainIDs)

	m.mu.RLock()
	if bundles, ok := m.cache[key]; ok {
		m.mu.RUnlock()
		return bundles, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if bundles, ok := m.cache[key]; ok {
		return bundles, nil
	}
	bundles, err := m.store.ListMatchableBundles(domainIDs)
	if err != nil {
		return nil, err
	}
	if m.cache == nil {
		m.cache = make(map[string][]ActivationBundle)
	}
	m.cache[key] = bundles
	return bundles, nil
}

// Match scores bundles against the Session ExpandedQuery — same hard-gate +
// exact-match shape as Matcher.Match, just over
// ActivationBundle.ObservedConditions.
// domainIDs scopes the candidate scan (2026-08-25, domain-sharded cache) —
// pass nil/empty when the query's domain is unresolved.
func (m *BundleMatcher) Match(_ context.Context, query session.ExpandedQuery, domainIDs []string, cfg MatchConfig) ([]BundleMatch, error) {
	cfg = cfg.withDefaults()

	bundles, err := m.loadCache(domainIDs)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	confCfg := m.confidenceCfg
	randFloat := m.randFloat
	m.mu.RUnlock()

	querySubject, qi, qa, qc := BuildQueryConditionTerms(query.Subject, query.Intent, query.Audience, query.Constraint)

	var results []BundleMatch
	for _, b := range bundles {
		conds := b.ObservedConditions
		if len(conds) == 0 {
			continue
		}
		owner, ok := findMatchingCondition(conds, querySubject, qi, qa, qc)
		if !ok {
			continue
		}
		tier, mean := conditionTier(owner, confCfg)
		base := BundleMatch{
			Bundle: b, Score: 1.0, MatchedBy: MatchedByExact,
			Tier: tier, Mean: mean,
			Subject: owner.Subject, Intent: owner.Intent, Audience: owner.Audience, Constraint: owner.Constraint,
		}
		switch tier {
		case TierExploring:
			if randFloat() < confCfg.ExploreRateLow {
				results = append(results, base)
			}
		case TierTrusted:
			base.AuditSampled = randFloat() < confCfg.ExploreRateTrusted
			results = append(results, base)
		default:
			base.AuditSampled = randFloat() < confCfg.ExploreRateSelfGraded
			results = append(results, base)
		}
	}

	sortBundleMatchesByMatchedByThenRecency(results)

	if len(results) > cfg.MatchTop {
		results = results[:cfg.MatchTop]
	}
	return results, nil
}
