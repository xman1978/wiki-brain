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
	Quality        string
	DirectPointIDs []string
	KPNCitedCount  int
	CitedCount     int
}

func gradeQuality(r *answer.AnswerResult) gradeResult {
	if r.EvidenceSet == nil {
		return gradeResult{Quality: QualityGap}
	}

	kpnCited, cited := kpnCitationCounts(r.EvidenceSet, r.Citations)
	directPointIDs := directCitedPointIDs(r.EvidenceSet, r.Citations)

	if len(directPointIDs) > 0 {
		return gradeResult{
			Quality:        QualityConfident,
			DirectPointIDs: directPointIDs,
			KPNCitedCount:  kpnCited,
			CitedCount:     cited,
		}
	}

	if len(r.EvidenceSet.Supporting) > 0 {
		return gradeResult{Quality: QualityPartial, KPNCitedCount: kpnCited, CitedCount: cited}
	}

	return gradeResult{Quality: QualityGap, KPNCitedCount: kpnCited, CitedCount: cited}
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

func directCitedPointIDs(es *retrieval.EvidenceSet, citations []string) []string {
	directFactToPoint := make(map[string]string, len(es.DirectEvidence))
	for _, e := range es.DirectEvidence {
		directFactToPoint[e.FactID] = e.PointID
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
