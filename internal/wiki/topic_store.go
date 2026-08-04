// Store queries backing topic-page candidate range detection
// (docs/impl/v1/wiki.md 步骤 8, 2026-08-03 修订).
package wiki

import (
	"database/sql"
	"fmt"
)

// QualifyingPointRef is the minimal shape topic-candidate detection needs
// out of the candidate-range KP retrieval: enough to group by entry_id
// (步骤 8 第 5 步) without pulling full QualifyingPoint content.
type QualifyingPointRef struct {
	PointID   string
	EntryID string
}

// QualifyingPointsByIDs filters an arbitrary point_id list down to KPs that
// are usable as *topic-scope material* (docs/impl/v1/wiki.md 步骤 8 第 4 步,
// 2026-08-04 修订): lifecycle=current on both KP and KU. Verified
// ActivationLink is intentionally NOT required here — topic-scope retrieval
// (manual POST /wiki/topics and Study DetectTopicCandidate) only decides
// "which current KPs fall in this theme", so a draft wiki can be assembled
// before usage has verified those links. Formalization still depends on
// verified signals: first-tier compile material (ListQualifyingPoints),
// entry-level ready, second-tier reliability, and publish (unless force).
//
// Naming note: historically this shared the "qualifying" label with
// ListQualifyingPoints (current + verified). Topic scope and one-tier
// compile now diverge on the verified leg; callers must not treat the two
// as interchangeable.
func (s *Store) QualifyingPointsByIDs(pointIDs []string) ([]QualifyingPointRef, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT kp.point_id, ku.entry_id
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.point_id IN (%s) AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			AND ku.entry_id IS NOT NULL`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: qualifying points by ids: %w", err)
	}
	defer rows.Close()

	var out []QualifyingPointRef
	for rows.Next() {
		var r QualifyingPointRef
		if err := rows.Scan(&r.PointID, &r.EntryID); err != nil {
			return nil, fmt.Errorf("wiki store: scan qualifying point ref: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasPendingWikiCandidate checks for an already-pending wiki_candidate
// learning_result for conceptID, so 步骤 6's "随批推进" member creation
// doesn't duplicate what Study's own flagWikiCandidates cycle may already
// have written.
func (s *Store) HasPendingWikiCandidate(conceptID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM learning_results
		WHERE action = 'wiki_candidate' AND object_type = 'wiki_page' AND object_id = ? AND status = 'pending_confirm' LIMIT 1`,
		conceptID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("wiki store: has pending wiki candidate: %w", err)
	}
	return true, nil
}

// PointDomainID resolves a KP's domain via its concept, for
// CreateTopicManual's optional domain_id scoping.
func (s *Store) PointDomainID(pointID string) (string, error) {
	var domainID string
	err := s.db.QueryRow(`
		SELECT c.domain_id FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN entries c ON ku.entry_id = c.entry_id
		WHERE kp.point_id = ?`, pointID).Scan(&domainID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("wiki store: point domain id: %w", err)
	}
	return domainID, nil
}

// VerifiedFraction implements 步骤 8 第 7 步"整体可靠度": the fraction of
// pointIDs (the full candidate-range retrieval result, not just the
// qualifying subset) that carry a verified ActivationLink.
func (s *Store) VerifiedFraction(pointIDs []string) (float64, error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}
	ph, args := buildPlaceholders(pointIDs)
	var verified int
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(DISTINCT point_id) FROM activation_links
		WHERE status = 'verified' AND point_id IN (%s)`, ph), args...).Scan(&verified)
	if err != nil {
		return 0, fmt.Errorf("wiki store: verified fraction: %w", err)
	}
	return float64(verified) / float64(len(pointIDs)), nil
}
