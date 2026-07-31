package graph

import (
	"reflect"
	"testing"
)

// TestCommunities_Deterministic asserts the same (nodes, edges, gamma) input
// always produces the same output partition (docs/impl/v1/wiki-generation.md
// 2.2 "确定性" — required so aspect clustering and cohesion scoring are
// stable across recompiles, not just across test runs).
func TestCommunities_Deterministic(t *testing.T) {
	nodes := []string{"p5", "p1", "p3", "p2", "p4"}
	edges := []Edge{
		{A: "p1", B: "p2", Weight: 2},
		{A: "p2", B: "p3", Weight: 2},
		{A: "p1", B: "p3", Weight: 2},
		{A: "p4", B: "p5", Weight: 2},
	}

	first := Communities(nodes, edges, 1.0)
	for i := 0; i < 5; i++ {
		got := Communities(nodes, edges, 1.0)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("Communities is not deterministic: run %d = %v, want %v", i, got, first)
		}
	}
}

// TestCommunities_TwoCliquesWeaklyLinked asserts that a graph shaped like
// two tightly-knit clusters joined by one weak bridge edge splits into two
// communities rather than collapsing into one — the specific failure mode
// connected-component analysis has on low-threshold KPN graphs
// (docs/impl/v1/wiki-generation.md 阶段 B, "为什么不能用连通分量").
func TestCommunities_TwoCliquesWeaklyLinked(t *testing.T) {
	nodes := []string{"a1", "a2", "a3", "a4", "b1", "b2", "b3", "b4"}
	var edges []Edge
	clique := func(members []string, w float64) {
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				edges = append(edges, Edge{A: members[i], B: members[j], Weight: w})
			}
		}
	}
	clique([]string{"a1", "a2", "a3", "a4"}, 3)
	clique([]string{"b1", "b2", "b3", "b4"}, 3)
	// One thin bridge — exactly the "relation_kpn_min=1 connects everything"
	// scenario the design doc calls out.
	edges = append(edges, Edge{A: "a4", B: "b1", Weight: 1})

	got := Communities(nodes, edges, 1.0)
	if len(got) < 2 {
		t.Fatalf("expected the weak bridge not to merge the two cliques into one community, got %v", got)
	}

	share := LargestShare(got)
	if share >= 0.75 {
		t.Fatalf("largest community share = %.2f, expected < 0.75 (no single community should dominate this graph)", share)
	}
}

// TestCommunities_SingleTightCluster asserts a genuinely single, densely
// connected cluster is NOT over-split into fragments — cohesion should
// report close to 1.0 for material that really is one topic.
func TestCommunities_SingleTightCluster(t *testing.T) {
	nodes := []string{"x1", "x2", "x3", "x4", "x5"}
	var edges []Edge
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			edges = append(edges, Edge{A: nodes[i], B: nodes[j], Weight: 2})
		}
	}

	got := Communities(nodes, edges, 1.0)
	share := LargestShare(got)
	if share < 0.99 {
		t.Fatalf("expected a single fully-connected cluster to stay together (share ~1.0), got %v, share=%.2f", got, share)
	}
}

// TestCommunities_IsolatedNodeBecomesSingleton asserts a node with no edges
// at all still appears in the output as its own community, never dropped.
func TestCommunities_IsolatedNodeBecomesSingleton(t *testing.T) {
	nodes := []string{"p1", "p2", "lonely"}
	edges := []Edge{{A: "p1", B: "p2", Weight: 1}}

	got := Communities(nodes, edges, 1.0)

	found := false
	for _, c := range got {
		for _, id := range c {
			if id == "lonely" {
				found = true
				if len(c) != 1 {
					t.Fatalf("expected 'lonely' to be a singleton community, got %v", c)
				}
			}
		}
	}
	if !found {
		t.Fatalf("isolated node 'lonely' missing from output: %v", got)
	}

	total := 0
	for _, c := range got {
		total += len(c)
	}
	if total != len(nodes) {
		t.Fatalf("output covers %d nodes, want %d (every input node must appear exactly once)", total, len(nodes))
	}
}

// TestCommunities_EmptyInput asserts no panic and a nil/empty result for no
// nodes.
func TestCommunities_EmptyInput(t *testing.T) {
	if got := Communities(nil, nil, 1.0); len(got) != 0 {
		t.Fatalf("expected empty result for empty input, got %v", got)
	}
}

// TestModularity_PositiveForRealStructure asserts the found partition scores
// higher modularity than the trivial "everything in one community" partition
// on a graph that actually has two separated clusters.
func TestModularity_PositiveForRealStructure(t *testing.T) {
	nodes := []string{"a1", "a2", "a3", "b1", "b2", "b3"}
	var edges []Edge
	clique := func(members []string, w float64) {
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				edges = append(edges, Edge{A: members[i], B: members[j], Weight: w})
			}
		}
	}
	clique([]string{"a1", "a2", "a3"}, 2)
	clique([]string{"b1", "b2", "b3"}, 2)
	edges = append(edges, Edge{A: "a3", B: "b1", Weight: 1})

	found := Communities(nodes, edges, 1.0)
	qFound := Modularity(nodes, edges, found, 1.0)
	qTrivial := Modularity(nodes, edges, [][]string{nodes}, 1.0)

	if qFound <= qTrivial {
		t.Fatalf("found partition modularity %.4f should exceed the single-community trivial partition %.4f", qFound, qTrivial)
	}
}
