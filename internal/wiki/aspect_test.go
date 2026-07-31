package wiki

import (
	"reflect"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/graph"
)

func aspectTestCfg() config.WikiConfig {
	return config.WikiConfig{
		AspectGamma:            1.0,
		AspectMinSize:          2,
		AspectMaxSize:          8,
		AspectSplitGammaFactor: 1.5,
	}
}

func points(ids ...string) []QualifyingPoint {
	out := make([]QualifyingPoint, len(ids))
	for i, id := range ids {
		out[i] = QualifyingPoint{PointID: id, UnitID: "u-" + id}
	}
	return out
}

func TestClusterAspects_Deterministic(t *testing.T) {
	pts := points("p1", "p2", "p3", "p4", "p5", "p6")
	edges := []graph.Edge{
		{A: "p1", B: "p2", Weight: 3},
		{A: "p2", B: "p3", Weight: 3},
		{A: "p1", B: "p3", Weight: 3},
		{A: "p4", B: "p5", Weight: 3},
		{A: "p5", B: "p6", Weight: 3},
		{A: "p4", B: "p6", Weight: 3},
	}
	cfg := aspectTestCfg()

	first := ClusterAspects(pts, edges, cfg)
	for i := 0; i < 5; i++ {
		got := ClusterAspects(pts, edges, cfg)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("ClusterAspects not deterministic: run %d = %+v, want %+v", i, got, first)
		}
	}
}

func TestClusterAspects_MinSizeMergesIntoStrongestNeighbor(t *testing.T) {
	// p1-p2-p3 form a tight triangle; p4 is a lone point weakly tied only to
	// p1. Alone p4 would be a singleton (< AspectMinSize=2) — it should
	// merge into the p1/p2/p3 aspect rather than staying its own community
	// or being silently dropped.
	pts := points("p1", "p2", "p3", "p4")
	edges := []graph.Edge{
		{A: "p1", B: "p2", Weight: 5},
		{A: "p2", B: "p3", Weight: 5},
		{A: "p1", B: "p3", Weight: 5},
		{A: "p1", B: "p4", Weight: 0.5},
	}
	cfg := aspectTestCfg()

	aspects := ClusterAspects(pts, edges, cfg)

	total := 0
	found4 := false
	for _, a := range aspects {
		total += len(a.PointIDs)
		for _, id := range a.PointIDs {
			if id == "p4" {
				found4 = true
				if len(a.PointIDs) < cfg.AspectMinSize {
					t.Fatalf("p4 ended up in an undersized aspect %+v", a)
				}
			}
		}
	}
	if !found4 {
		t.Fatalf("p4 missing from clustering output: %+v", aspects)
	}
	if total != len(pts) {
		t.Fatalf("aspect output covers %d points, want %d", total, len(pts))
	}
}

func TestClusterAspects_IsolatedPointBecomesMisc(t *testing.T) {
	// "lonely" has no edges to anything — mergeUndersized can't fold it
	// anywhere, so it must surface as its own (necessarily < min size)
	// community, which ClusterAspects tags "misc".
	pts := points("p1", "p2", "lonely")
	edges := []graph.Edge{
		{A: "p1", B: "p2", Weight: 2},
	}
	cfg := aspectTestCfg()

	aspects := ClusterAspects(pts, edges, cfg)

	var miscCount int
	foundLonely := false
	for _, a := range aspects {
		if a.AspectID == aspectMiscID {
			miscCount++
			for _, id := range a.PointIDs {
				if id == "lonely" {
					foundLonely = true
				}
			}
		} else if len(a.PointIDs) < cfg.AspectMinSize {
			t.Fatalf("non-misc aspect %+v is under min size", a)
		}
	}
	if !foundLonely {
		t.Fatalf("lonely point not folded into misc: %+v", aspects)
	}
	if miscCount > 1 {
		t.Fatalf("expected at most one misc bucket, got %d", miscCount)
	}
}

func TestClusterAspects_OversizedSplitsRecursively(t *testing.T) {
	// Two 5-node cliques bridged weakly, with AspectMaxSize=8 — one big
	// community would exceed max size and must be split, not left oversized.
	ids := []string{"a1", "a2", "a3", "a4", "a5", "b1", "b2", "b3", "b4", "b5"}
	pts := points(ids...)
	var edges []graph.Edge
	clique := func(members []string, w float64) {
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				edges = append(edges, graph.Edge{A: members[i], B: members[j], Weight: w})
			}
		}
	}
	clique([]string{"a1", "a2", "a3", "a4", "a5"}, 3)
	clique([]string{"b1", "b2", "b3", "b4", "b5"}, 3)
	edges = append(edges, graph.Edge{A: "a5", B: "b1", Weight: 0.5})

	cfg := aspectTestCfg()
	cfg.AspectMaxSize = 8

	aspects := ClusterAspects(pts, edges, cfg)

	total := 0
	for _, a := range aspects {
		total += len(a.PointIDs)
		if len(a.PointIDs) > cfg.AspectMaxSize {
			t.Fatalf("aspect %+v exceeds AspectMaxSize=%d", a, cfg.AspectMaxSize)
		}
	}
	if total != len(ids) {
		t.Fatalf("aspect output covers %d points, want %d", total, len(ids))
	}
}

func TestBuildAspectEdges_ContradictsCountsPositive(t *testing.T) {
	pts := points("p1", "p2")
	cfg := config.WikiConfig{AspectWRel: 1.0}
	kpnPairs := map[[2]string]bool{edgeKeyPair("p1", "p2"): true}

	edges := BuildAspectEdges(pts, kpnPairs, nil, nil, cfg)
	if len(edges) != 1 || edges[0].Weight <= 0 {
		t.Fatalf("expected one positive-weight edge for a KPN pair (related or contradicts), got %+v", edges)
	}
}
