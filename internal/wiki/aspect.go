package wiki

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/graph"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// buildAspects wires the DB-backed signals (KPN relations, question
// co-occurrence, verified-link intents) into BuildAspectEdges + ClusterAspects
// for a concept's qualifying points, then assigns each resulting aspect its
// program-suggested name (2.3). Point-count edge fan-out here is O(n^2) over
// qualifying KPs, capped in practice by wiki.compile_max_chars truncation.
func (s *Service) buildAspects(points []QualifyingPoint) ([]Aspect, error) {
	if len(points) == 0 {
		return nil, nil
	}
	pointIDs := make([]string, len(points))
	for i, p := range points {
		pointIDs[i] = p.PointID
	}

	rels, err := s.store.RelationsAmong(pointIDs)
	if err != nil {
		return nil, fmt.Errorf("wiki: aspect relations: %w", err)
	}
	kpnPairs := make(map[[2]string]bool, len(rels))
	for _, r := range rels {
		kpnPairs[edgeKeyPair(r.SourcePointID, r.TargetPointID)] = true
	}

	coocCounts, err := s.store.CooccurrencePairs(pointIDs)
	if err != nil {
		return nil, fmt.Errorf("wiki: aspect cooccurrence: %w", err)
	}

	intents, err := s.store.PointIntents(pointIDs)
	if err != nil {
		return nil, fmt.Errorf("wiki: aspect intents: %w", err)
	}

	edges := BuildAspectEdges(points, kpnPairs, coocCounts, intents, s.cfg)
	aspects := ClusterAspects(points, edges, s.cfg)
	for i := range aspects {
		aspects[i].SuggestedName = SuggestAspectName(points, aspects[i].PointIDs, intents)
	}
	return aspects, nil
}

// Aspect is one leaf community produced by ClusterAspects — the structural
// unit between "concept" (too coarse) and "KP" (too fine) that the analyze
// stage organizes claims by and the compile stage organizes "展开说明" by
// (docs/impl/v1/wiki-generation.md 阶段 B). This is the in-memory clustering
// output; wiki_pages.aspects persists a PageAspect projection of it after
// compilation (6.4).
type Aspect struct {
	AspectID      string
	SuggestedName string
	PointIDs      []string
}

// aspectMiscID is the reserved bucket for points that end up in no viable
// aspect (isolated nodes, or leftovers of a merge with nowhere strong to go)
// — 2.2 "后处理": misc never becomes its own outline section.
const aspectMiscID = "misc"

// BuildAspectEdges implements the edge-weight formula in 2.1 exactly:
//
//	w(p,q) = w_rel * [related(p,q) or contradicts(p,q)]
//	       + w_cooc * min(1, cooc_questions(p,q) / cooc_sat)
//	       + w_intent * Jaccard(intents(p), intents(q))
//	       + w_unit * [unit_id(p) == unit_id(q)]
//
// kpnPairs and coocCounts are unordered-pair keyed (edgeKeyPair(a,b) with
// a<b); both relation types count as the same positive w_rel term — see the
// design doc's "contradicts 计正权，不是负权": two KPs contradicting each
// other are still, structurally, talking about the same thing. This is a
// pure function so aaspect determinism is directly testable without a DB.
func BuildAspectEdges(points []QualifyingPoint, kpnPairs map[[2]string]bool, coocCounts map[[2]string]int, intents map[string][]string, cfg config.WikiConfig) []graph.Edge {
	wRel := cfg.AspectWRel
	wCooc := cfg.AspectWCooc
	wIntent := cfg.AspectWIntent
	wUnit := cfg.AspectWUnit
	coocSat := cfg.AspectCoocSat
	if coocSat <= 0 {
		coocSat = 3
	}

	intentSets := make(map[string]map[string]bool, len(points))
	for p, ins := range intents {
		set := make(map[string]bool, len(ins))
		for _, in := range ins {
			in = strings.TrimSpace(in)
			if in != "" {
				set[in] = true
			}
		}
		intentSets[p] = set
	}

	weights := make(map[[2]string]float64)
	n := len(points)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			p, q := points[i].PointID, points[j].PointID
			k := edgeKeyPair(p, q)

			var w float64
			if kpnPairs[k] {
				w += wRel
			}
			if c := coocCounts[k]; c > 0 {
				ratio := float64(c) / float64(coocSat)
				if ratio > 1 {
					ratio = 1
				}
				w += wCooc * ratio
			}
			if j1 := jaccard(intentSets[p], intentSets[q]); j1 > 0 {
				w += wIntent * j1
			}
			if points[i].UnitID != "" && points[i].UnitID == points[j].UnitID {
				w += wUnit
			}
			if w > 0 {
				weights[k] = w
			}
		}
	}

	edges := make([]graph.Edge, 0, len(weights))
	for k, w := range weights {
		edges = append(edges, graph.Edge{A: k[0], B: k[1], Weight: w})
	}
	return edges
}

func edgeKeyPair(a, b string) [2]string {
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ClusterAspects runs Louvain community detection over the concept's
// qualifying KPs and applies the min/max-size post-processing from 2.2:
// undersized communities merge into whichever remaining community they hold
// the most edge weight with (falling back to "misc" if they have none),
// oversized communities are recursively re-clustered once at a higher
// resolution, and any still-oversized result after that is split by unit_id.
//
// Node traversal is always point_id lexical order (inherited from
// graph.Communities), so the same (points, edges, cfg) input is
// deterministic — required by the completion criteria in 14.
func ClusterAspects(points []QualifyingPoint, edges []graph.Edge, cfg config.WikiConfig) []Aspect {
	if len(points) == 0 {
		return nil
	}

	ids := make([]string, len(points))
	unitOf := make(map[string]string, len(points))
	for i, p := range points {
		ids[i] = p.PointID
		unitOf[p.PointID] = p.UnitID
	}

	gamma := cfg.AspectGamma
	if gamma <= 0 {
		gamma = 1
	}
	minSize := cfg.AspectMinSize
	if minSize <= 0 {
		minSize = 2
	}
	maxSize := cfg.AspectMaxSize
	if maxSize <= 0 {
		maxSize = 8
	}
	splitFactor := cfg.AspectSplitGammaFactor
	if splitFactor <= 0 {
		splitFactor = 1.5
	}

	communities := graph.Communities(ids, edges, gamma)
	communities = splitOversized(communities, edges, gamma*splitFactor, maxSize, unitOf)
	communities = mergeUndersized(communities, edges, minSize)

	sort.Slice(communities, func(i, j int) bool { return communities[i][0] < communities[j][0] })

	// Every leftover under-min community consolidates into ONE reserved
	// "misc" bucket — 2.2 describes misc as a single bucket, not a
	// per-leftover-community label; without this, two unrelated isolated
	// points would each surface as their own community both (confusingly)
	// named "misc" instead of being combined.
	var misc []string
	aspects := make([]Aspect, 0, len(communities))
	n := 0
	for _, members := range communities {
		if len(members) < minSize {
			misc = append(misc, members...)
			continue
		}
		n++
		aspects = append(aspects, Aspect{AspectID: fmt.Sprintf("a%d", n), PointIDs: members})
	}
	if len(misc) > 0 {
		sort.Strings(misc)
		aspects = append(aspects, Aspect{AspectID: aspectMiscID, PointIDs: misc})
	}
	return aspects
}

// splitOversized recursively re-clusters (at most one extra level, per 2.2)
// any community larger than maxSize; a community still oversized after that
// is cut deterministically by unit_id groups.
func splitOversized(communities [][]string, edges []graph.Edge, splitGamma float64, maxSize int, unitOf map[string]string) [][]string {
	var out [][]string
	for _, members := range communities {
		if len(members) <= maxSize {
			out = append(out, members)
			continue
		}
		subEdges := subsetEdges(edges, members)
		sub := graph.Communities(members, subEdges, splitGamma)
		for _, s := range sub {
			if len(s) <= maxSize {
				out = append(out, s)
				continue
			}
			out = append(out, splitByUnit(s, unitOf, maxSize)...)
		}
	}
	return out
}

func splitByUnit(members []string, unitOf map[string]string, maxSize int) [][]string {
	byUnit := make(map[string][]string)
	order := make([]string, 0)
	for _, m := range members {
		u := unitOf[m]
		if _, ok := byUnit[u]; !ok {
			order = append(order, u)
		}
		byUnit[u] = append(byUnit[u], m)
	}
	sort.Strings(order)
	var out [][]string
	var cur []string
	for _, u := range order {
		group := byUnit[u]
		sort.Strings(group)
		if len(cur)+len(group) > maxSize && len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, group...)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func subsetEdges(edges []graph.Edge, members []string) []graph.Edge {
	set := make(map[string]bool, len(members))
	for _, m := range members {
		set[m] = true
	}
	var out []graph.Edge
	for _, e := range edges {
		if set[e.A] && set[e.B] {
			out = append(out, e)
		}
	}
	return out
}

// mergeUndersized folds communities smaller than minSize into whichever
// remaining community they hold the strongest total edge weight with; a
// community with no edge to anything remaining (a genuinely isolated point,
// or a small cluster only connected to other small clusters that all end up
// merged away) is left as-is — ClusterAspects tags it "misc" by size.
func mergeUndersized(communities [][]string, edges []graph.Edge, minSize int) [][]string {
	memberOf := make(map[string]int, 0)
	kept := make([][]string, len(communities))
	copy(kept, communities)
	for i, members := range kept {
		for _, m := range members {
			memberOf[m] = i
		}
	}

	// Deterministic order: smallest, then lexically-first-member first.
	order := make([]int, 0, len(kept))
	for i := range kept {
		order = append(order, i)
	}
	sort.Slice(order, func(a, b int) bool {
		ca, cb := kept[order[a]], kept[order[b]]
		if len(ca) != len(cb) {
			return len(ca) < len(cb)
		}
		return ca[0] < cb[0]
	})

	merged := make(map[int]bool)
	for _, ci := range order {
		if merged[ci] || len(kept[ci]) >= minSize {
			continue
		}
		weight := make(map[int]float64)
		for _, e := range edges {
			ai, aok := memberOfCommunity(kept, merged, e.A)
			bi, bok := memberOfCommunity(kept, merged, e.B)
			if !aok || !bok {
				continue
			}
			if ai == ci && bi != ci {
				weight[bi] += e.Weight
			} else if bi == ci && ai != ci {
				weight[ai] += e.Weight
			}
		}
		if len(weight) == 0 {
			continue // no edges to anything else — stays small, tagged misc by size
		}
		targets := make([]int, 0, len(weight))
		for t := range weight {
			targets = append(targets, t)
		}
		sort.Slice(targets, func(a, b int) bool {
			if weight[targets[a]] != weight[targets[b]] {
				return weight[targets[a]] > weight[targets[b]]
			}
			return kept[targets[a]][0] < kept[targets[b]][0]
		})
		best := targets[0]
		kept[best] = append(kept[best], kept[ci]...)
		sort.Strings(kept[best])
		kept[ci] = nil
		merged[ci] = true
	}

	var out [][]string
	for i, members := range kept {
		if merged[i] || len(members) == 0 {
			continue
		}
		out = append(out, members)
	}
	return out
}

func memberOfCommunity(communities [][]string, merged map[int]bool, node string) (int, bool) {
	for i, members := range communities {
		if merged[i] {
			continue
		}
		for _, m := range members {
			if m == node {
				return i, true
			}
		}
	}
	return 0, false
}

// SuggestAspectName implements 2.3: candidate name = top-2 high-frequency
// words across the aspect's KPs' KU centers (gse-segmented), plus the
// aspect's most frequent intent (if any verified-link intents are present).
// This is only a suggestion — the outline-stage LLM may rewrite it, but the
// program guarantees it's never empty.
func SuggestAspectName(points []QualifyingPoint, pointIDs []string, intents map[string][]string) string {
	inSet := make(map[string]bool, len(pointIDs))
	for _, id := range pointIDs {
		inSet[id] = true
	}

	wordFreq := make(map[string]int)
	for _, p := range points {
		if !inSet[p.PointID] {
			continue
		}
		for _, w := range text.Tokenize(p.UnitCenter) {
			w = strings.TrimSpace(w)
			if len([]rune(w)) < 2 {
				continue
			}
			wordFreq[w]++
		}
	}
	topWords := topN(wordFreq, 2)

	intentFreq := make(map[string]int)
	for _, id := range pointIDs {
		for _, in := range intents[id] {
			in = strings.TrimSpace(in)
			if in != "" {
				intentFreq[in]++
			}
		}
	}
	topIntent := topN(intentFreq, 1)

	parts := append(topWords, topIntent...)
	if len(parts) == 0 {
		return aspectMiscID
	}
	return strings.Join(parts, " · ")
}

func topN(freq map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(freq))
	for k, v := range freq {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, len(list))
	for i, kv := range list {
		out[i] = kv.k
	}
	return out
}
