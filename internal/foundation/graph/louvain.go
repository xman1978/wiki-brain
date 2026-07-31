// Package graph provides a small, dependency-free weighted community
// detection primitive (Louvain-style modularity optimization). It is shared
// by concept cohesion scoring (internal/study) and, in a later phase, Wiki
// aspect clustering (internal/wiki) — see
// docs/impl/v1/wiki-generation.md 阶段 B "为什么不能用连通分量": on a graph
// where a single edge is enough to connect two nodes (which is exactly the
// KPN relation_kpn_min=1 case), plain connected-component analysis collapses
// almost any non-trivial concept into one giant component and tells you
// nothing about whether it actually holds together as one topic. Modularity
// optimization asks a different, more useful question: is there a grouping
// where edges are denser within groups than chance would predict.
//
// This package is deliberately generic: callers supply string node ids and
// weighted undirected edges, and get back a partition. It has no notion of
// what a "node" is (a knowledge point, a wiki page, ...) — that mapping is
// the caller's job.
package graph

import "sort"

// Edge is one weighted, undirected edge between two node ids. A and B may be
// given in either order. Self-loops (A == B) and non-positive weights are
// ignored by every function in this package.
type Edge struct {
	A, B   string
	Weight float64
}

// Communities runs deterministic weighted Louvain modularity optimization
// and returns the resulting partition: a list of communities, each a sorted
// list of node ids. Every id in nodeIDs appears in exactly one community,
// including isolated nodes (each becomes a singleton community of size 1) —
// callers that want a "misc" bucket for undersized communities apply that
// policy themselves on top of this result
// (docs/impl/v1/wiki-generation.md 2.2 "后处理").
//
// gamma is the resolution parameter: gamma=1 is standard modularity; gamma>1
// biases toward more, smaller communities (docs/impl/v1/wiki-generation.md
// "aspect_gamma"/"aspect_split_gamma_factor"). gamma<=0 is treated as 1.
//
// Determinism: node and community iteration order is always ascending
// lexical order of node id, and gain ties are broken by the smallest
// candidate community's representative id — so the same (nodeIDs, edges,
// gamma) input always produces the same output partition. This is required
// for aspect clustering and cohesion scoring to be testable and stable
// across recompiles (docs/impl/v1/wiki-generation.md 2.2 "确定性").
func Communities(nodeIDs []string, edges []Edge, gamma float64) [][]string {
	if len(nodeIDs) == 0 {
		return nil
	}
	if gamma <= 0 {
		gamma = 1
	}

	ids := make([]string, 0, len(nodeIDs))
	seen := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil
	}

	validEdges := make([]Edge, 0, len(edges))
	for _, e := range edges {
		if e.A == e.B || e.Weight <= 0 {
			continue
		}
		if !seen[e.A] || !seen[e.B] {
			continue // ignore edges referencing ids outside nodeIDs
		}
		validEdges = append(validEdges, e)
	}

	// members[level-node-id] = the original node ids folded into it so far.
	members := make(map[string][]string, len(ids))
	for _, id := range ids {
		members[id] = []string{id}
	}

	curNodes := ids
	curEdges := validEdges
	// selfLoop[node] = total weight of edges that are already "inside" this
	// (possibly aggregated) node from earlier folds. This must be carried
	// forward and folded into each node's degree — dropping it would make
	// higher levels blind to how much mass a community already represents,
	// so a single weak bridge edge between two big aggregated communities
	// would always look modularity-improving in the reduced graph even
	// when merging them is a net loss overall.
	selfLoop := make(map[string]float64, len(ids))

	// Guard against any theoretical non-termination from floating point
	// edge cases — real convergence happens in a handful of folds.
	for level := 0; level < 50; level++ {
		assign := localMove(curNodes, curEdges, selfLoop, gamma)

		distinct := make(map[string]bool, len(curNodes))
		for _, c := range assign {
			distinct[c] = true
		}
		if len(distinct) == len(curNodes) {
			// No node moved out of its own singleton community this level —
			// nothing left to fold, current members/curNodes is final.
			break
		}

		nextMembers := make(map[string][]string, len(distinct))
		for i, node := range curNodes {
			c := assign[i]
			nextMembers[c] = append(nextMembers[c], members[node]...)
		}
		members = nextMembers

		nextNodes := make([]string, 0, len(nextMembers))
		for c := range nextMembers {
			nextNodes = append(nextNodes, c)
		}
		sort.Strings(nextNodes)

		nodeIndex := make(map[string]int, len(curNodes))
		for i, n := range curNodes {
			nodeIndex[n] = i
		}
		nextSelfLoop := make(map[string]float64, len(nextNodes))
		for i, node := range curNodes {
			nextSelfLoop[assign[i]] += selfLoop[node]
		}
		aggWeight := make(map[[2]string]float64)
		for _, e := range curEdges {
			ca, cb := assign[nodeIndex[e.A]], assign[nodeIndex[e.B]]
			if ca == cb {
				// Internal edge — folds into the merged node's self-loop so
				// its mass isn't lost to the next level's degree accounting.
				nextSelfLoop[ca] += e.Weight
				continue
			}
			aggWeight[edgeKey(ca, cb)] += e.Weight
		}
		nextEdges := make([]Edge, 0, len(aggWeight))
		for k, w := range aggWeight {
			nextEdges = append(nextEdges, Edge{A: k[0], B: k[1], Weight: w})
		}
		selfLoop = nextSelfLoop

		if len(nextNodes) == len(curNodes) {
			// Shouldn't happen given the distinct-count check above, but
			// guards against an infinite loop if it ever does.
			curNodes, curEdges = nextNodes, nextEdges
			break
		}
		curNodes, curEdges = nextNodes, nextEdges
	}

	out := make([][]string, 0, len(curNodes))
	for _, n := range curNodes {
		m := append([]string(nil), members[n]...)
		sort.Strings(m)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func edgeKey(a, b string) [2]string {
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// localMove runs one level's local-moving phase to convergence: repeatedly
// scan nodes (in the given, already-sorted order) and move each into
// whichever neighboring community (or its own) maximizes modularity gain,
// until a full pass produces no move. Returns, for each input node (by
// index), the representative id of the community it ended up in.
func localMove(nodes []string, edges []Edge, selfLoop map[string]float64, gamma float64) []string {
	n := len(nodes)
	idx := make(map[string]int, n)
	for i, node := range nodes {
		idx[node] = i
	}

	adj := make([]map[int]float64, n)
	for i := range adj {
		adj[i] = make(map[int]float64)
	}
	degree := make([]float64, n)
	m2 := 0.0
	for i, node := range nodes {
		d := 2 * selfLoop[node]
		degree[i] += d
		m2 += d
	}
	for _, e := range edges {
		ai, aok := idx[e.A]
		bi, bok := idx[e.B]
		if !aok || !bok {
			continue
		}
		adj[ai][bi] += e.Weight
		adj[bi][ai] += e.Weight
		degree[ai] += e.Weight
		degree[bi] += e.Weight
		m2 += 2 * e.Weight
	}

	comm := make([]int, n) // node index -> community, identified by a node index
	commTot := make([]float64, n)
	for i := range nodes {
		comm[i] = i
		commTot[i] = degree[i]
	}

	out := func() []string {
		res := make([]string, n)
		for i := range nodes {
			res[i] = nodes[comm[i]]
		}
		return res
	}

	if m2 == 0 {
		return out() // no edges at all — every node its own community
	}

	for pass := 0; pass < 100; pass++ {
		improved := false
		for i := 0; i < n; i++ {
			ci := comm[i]
			commTot[ci] -= degree[i]

			neighWeight := make(map[int]float64, len(adj[i]))
			for j, w := range adj[i] {
				neighWeight[comm[j]] += w
			}

			candSet := map[int]bool{ci: true}
			for cj := range neighWeight {
				candSet[cj] = true
			}
			cands := make([]int, 0, len(candSet))
			for c := range candSet {
				cands = append(cands, c)
			}
			sort.Slice(cands, func(a, b int) bool { return nodes[cands[a]] < nodes[cands[b]] })

			bestComm := ci
			bestGain := neighWeight[ci] - gamma*commTot[ci]*degree[i]/m2
			for _, c := range cands {
				if c == ci {
					continue
				}
				gain := neighWeight[c] - gamma*commTot[c]*degree[i]/m2
				if gain > bestGain+1e-9 {
					bestGain = gain
					bestComm = c
				}
			}

			commTot[bestComm] += degree[i]
			if bestComm != ci {
				comm[i] = bestComm
				improved = true
			}
		}
		if !improved {
			break
		}
	}

	return out()
}

// Modularity computes the weighted modularity Q of a given partition over
// (nodeIDs, edges) — mainly useful for tests and for surfacing "how strong
// is this community structure" alongside a Communities() result.
func Modularity(nodeIDs []string, edges []Edge, partition [][]string, gamma float64) float64 {
	if gamma <= 0 {
		gamma = 1
	}
	valid := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		valid[id] = true
	}
	commOf := make(map[string]int, len(nodeIDs))
	for ci, comm := range partition {
		for _, id := range comm {
			commOf[id] = ci
		}
	}

	degree := make(map[string]float64, len(nodeIDs))
	within := make(map[int]float64, len(partition))
	m2 := 0.0
	for _, e := range edges {
		if e.A == e.B || e.Weight <= 0 || !valid[e.A] || !valid[e.B] {
			continue
		}
		degree[e.A] += e.Weight
		degree[e.B] += e.Weight
		m2 += 2 * e.Weight
		if ca, ok := commOf[e.A]; ok {
			if cb, ok2 := commOf[e.B]; ok2 && ca == cb {
				within[ca] += 2 * e.Weight
			}
		}
	}
	if m2 == 0 {
		return 0
	}

	q := 0.0
	for ci, comm := range partition {
		tot := 0.0
		for _, id := range comm {
			tot += degree[id]
		}
		q += within[ci]/m2 - gamma*(tot/m2)*(tot/m2)
	}
	return q
}

// LargestShare returns the size of the largest community in partition
// divided by the total number of nodes across all communities — the
// "cohesion" metric of docs/impl/v1/wiki-generation.md 2.4: how much of the
// concept's qualifying material sits in one coherent group versus being
// scattered across several unrelated ones. Returns 0 for an empty partition.
func LargestShare(partition [][]string) float64 {
	total := 0
	max := 0
	for _, c := range partition {
		total += len(c)
		if len(c) > max {
			max = len(c)
		}
	}
	if total == 0 {
		return 0
	}
	return float64(max) / float64(total)
}
