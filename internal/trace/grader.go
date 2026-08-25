package trace

import (
	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

const (
	QualityConfident = "confident"
	QualityPartial   = "partial"
	QualityGap       = "gap"
)

type gradeResult struct {
	Quality           string
	DirectPointIDs    []string
	KPNCitedCount     int
	CitedCount        int
	OutlineCitedCount int
	CitedRankSum      int
	// CitedRankMax is the worst (largest) mergedRank among cited evidence —
	// the conservative R* proxy for top-N/系数自收敛 calibration
	// (docs/design/topn-coefficient-convergence.md 第 2 节). -1 when no
	// full-path evidence was cited.
	CitedRankMax int
}

func gradeQuality(r *answer.AnswerResult) gradeResult {
	if r.EvidenceSet == nil {
		return gradeResult{Quality: QualityGap}
	}

	if r.EvidenceSet.PathType == retrieval.PathTypeWiki {
		if len(r.EvidenceSet.CitedPointIDs) > 0 {
			return gradeResult{Quality: QualityConfident, DirectPointIDs: r.EvidenceSet.CitedPointIDs}
		}
		return gradeResult{Quality: QualityPartial}
	}

	kpnCited, cited := kpnCitationCounts(r.EvidenceSet, r.Citations)
	directPointIDs := directCitedPointIDs(r.EvidenceSet, r.Citations)
	outlineCited, rankSum, rankMax := recallCitationStats(r.EvidenceSet, r.Citations)

	if len(directPointIDs) > 0 {
		return gradeResult{
			Quality:           QualityConfident,
			DirectPointIDs:    directPointIDs,
			KPNCitedCount:     kpnCited,
			CitedCount:        cited,
			OutlineCitedCount: outlineCited,
			CitedRankSum:      rankSum,
			CitedRankMax:      rankMax,
		}
	}

	if len(r.EvidenceSet.Supporting) > 0 {
		return gradeResult{Quality: QualityPartial, KPNCitedCount: kpnCited, CitedCount: cited, OutlineCitedCount: outlineCited, CitedRankSum: rankSum, CitedRankMax: rankMax}
	}

	return gradeResult{Quality: QualityGap, KPNCitedCount: kpnCited, CitedCount: cited, OutlineCitedCount: outlineCited, CitedRankSum: rankSum, CitedRankMax: rankMax}
}

// kpnCitationCounts reports, among the fact_ids Answer actually cited, how many
// resolve to Evidence in the EvidenceSet (cited) and how many of those originated
// from KPN expansion rather than Rerank (kpnCited). Computed independently of the
// confident/partial/gap split — a confident trace can still cite a KPN-origin
// supporting fact alongside its direct citation.
func kpnCitationCounts(es *retrieval.EvidenceSet, citations []string) (kpnCited, cited int) {
	origin := make(map[string]string, len(es.DirectEvidence)+len(es.Supporting))
	for _, e := range es.DirectEvidence {
		origin[e.FactID] = e.Origin
	}
	for _, e := range es.Supporting {
		origin[e.FactID] = e.Origin
	}

	seen := make(map[string]bool, len(citations))
	for _, fid := range citations {
		if seen[fid] {
			continue
		}
		o, ok := origin[fid]
		if !ok {
			continue
		}
		seen[fid] = true
		cited++
		if o == retrieval.OriginKPNExpansion {
			kpnCited++
		}
	}
	return kpnCited, cited
}

// recallCitationStats reports, among the fact_ids Answer actually cited on
// a full-path (rrfMerge-recalled) trace, how many originated from outline
// (目录结构) recall and the sum of their rank in the RRF-merged list
// (0-based, pre rerank_top_n truncation). Paired with the existing
// cited_count column this gives avg rank = cited_rank_sum / cited_count —
// data to check whether outline recall really does rank truer hits higher
// than FTS, and whether rerank_top_n can be tightened without losing them
// (2026-08-09 决策，见 chat). Fast-path evidence bypasses rrfMerge, so its
// RecallPaths/MergedRank are always zero-value — only path_type=full trace
// carries meaningful signal here.
func recallCitationStats(es *retrieval.EvidenceSet, citations []string) (outlineCited, rankSum, rankMax int) {
	rankMax = -1
	if es.PathType != retrieval.PathTypeFull {
		return 0, 0, rankMax
	}
	type recallInfo struct {
		paths []string
		rank  int
	}
	byFactID := make(map[string]recallInfo, len(es.DirectEvidence)+len(es.Supporting))
	for _, e := range es.DirectEvidence {
		byFactID[e.FactID] = recallInfo{paths: e.RecallPaths, rank: e.MergedRank}
	}
	for _, e := range es.Supporting {
		byFactID[e.FactID] = recallInfo{paths: e.RecallPaths, rank: e.MergedRank}
	}

	seen := make(map[string]bool, len(citations))
	for _, fid := range citations {
		if seen[fid] {
			continue
		}
		info, ok := byFactID[fid]
		if !ok {
			continue
		}
		seen[fid] = true
		rankSum += info.rank
		if info.rank > rankMax {
			rankMax = info.rank
		}
		for _, p := range info.paths {
			if p == "outline" {
				outlineCited++
				break
			}
		}
	}
	return outlineCited, rankSum, rankMax
}

// directCitedPointIDs resolves cited fact_ids to point_ids using the union of
// DirectEvidence and Supporting — a multi-step full-path answer regularly
// cites only Supporting evidence (rerank's direct/supporting split is a rank
// bucket, not a correctness judgment; e.g. the deep-reasoning cases where the
// model finds its answer in a supporting fact), and treating those citations
// as "no direct evidence" wrongly downgrades an otherwise confident answer to
// partial, which then suppresses the activation_gap learning signal for it.
func directCitedPointIDs(es *retrieval.EvidenceSet, citations []string) []string {
	directFactToPoint := make(map[string]string, len(es.DirectEvidence)+len(es.Supporting))
	for _, e := range es.DirectEvidence {
		directFactToPoint[e.FactID] = e.PointID
	}
	for _, e := range es.Supporting {
		if _, ok := directFactToPoint[e.FactID]; !ok {
			directFactToPoint[e.FactID] = e.PointID
		}
	}

	seen := make(map[string]bool)
	var result []string
	for _, fid := range citations {
		pid, ok := directFactToPoint[fid]
		if ok && !seen[pid] {
			seen[pid] = true
			result = append(result, pid)
		}
	}
	return result
}
