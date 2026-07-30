package unit

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var reflowPointIDTagRe = regexp.MustCompile(`\[([^\[\]\s]+)\]`)

// AncestorPointIDsForWikiPage implements docs/impl/v1/wiki.md 步骤 10's
// self-ancestor set: a Wiki page's current source_point_ids, unioned with
// every point_id ever cited in its wiki_revisions history (a KP once cited
// by an earlier revision and since dropped is still "the same knowledge" for
// reflow-exclusion purposes — docs/impl/v1/kpn.md 步骤 2 "自体祖先排除" reads
// "含该页面历史 revision 引用过的 point_id").
func (s *Store) AncestorPointIDsForWikiPage(pageID string) ([]string, error) {
	seen := make(map[string]bool)
	var out []string

	var sourcePointIDsJSON string
	err := s.db.QueryRow(`SELECT source_point_ids FROM wiki_pages WHERE page_id = ?`, pageID).Scan(&sourcePointIDsJSON)
	if err != nil {
		return nil, fmt.Errorf("unit store: ancestor point ids: get page: %w", err)
	}
	var current []string
	json.Unmarshal([]byte(sourcePointIDsJSON), &current)
	for _, id := range current {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}

	rows, err := s.db.Query(`SELECT content FROM wiki_revisions WHERE page_id = ?`, pageID)
	if err != nil {
		return nil, fmt.Errorf("unit store: ancestor point ids: list revisions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, fmt.Errorf("unit store: ancestor point ids: scan revision: %w", err)
		}
		for _, m := range reflowPointIDTagRe.FindAllStringSubmatch(content, -1) {
			id := m[1]
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, rows.Err()
}
