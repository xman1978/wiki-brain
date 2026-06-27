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
}

func gradeQuality(r *answer.AnswerResult) gradeResult {
	if r.EvidenceSet == nil {
		return gradeResult{Quality: QualityGap}
	}

	directPointIDs := directCitedPointIDs(r.EvidenceSet, r.Citations)

	if len(directPointIDs) > 0 {
		return gradeResult{
			Quality:        QualityConfident,
			DirectPointIDs: directPointIDs,
		}
	}

	if len(r.EvidenceSet.Supporting) > 0 {
		return gradeResult{Quality: QualityPartial}
	}

	return gradeResult{Quality: QualityGap}
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
