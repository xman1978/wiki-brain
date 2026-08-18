// Writing drafts (docs/impl/v1/wiki.md 步骤 10): page-derived, freely
// editable writing surfaces. wiki_pages.content remains compile-only —
// there is no draft -> page write-back path anywhere in this file.
package wiki

import (
	"encoding/json"
	"fmt"
)

// CreateDraft implements docs/impl/v1/wiki.md 步骤 10:
// POST /wiki/pages/:id/drafts.
func (s *Service) CreateDraft(pageID, mode string) (*Draft, error) {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	revisions, err := s.store.ListRevisions(pageID)
	if err != nil {
		return nil, err
	}
	if len(revisions) == 0 {
		return nil, fmt.Errorf("wiki: page %s has no revisions to derive a draft from", pageID)
	}
	latestRevision := revisions[len(revisions)-1]

	// 单层化（docs/impl/v1/wiki-single-tier-task-brief.md）: 一次编译请求直接
	// 产出一份成品页面，不再有"主题页聚合成员页面"这回事，assembled 模式随
	// contains 一起废弃 — 一律按 page 模式取当前页面正文。
	if mode == "" {
		mode = DraftModePage
	}
	if mode != DraftModePage {
		return nil, fmt.Errorf("wiki: invalid draft mode %q", mode)
	}

	sourcePageIDs := []string{pageID}
	content := latestRevision.Content
	title := page.Title

	evidenceIndex, err := s.buildEvidenceIndex(sourcePageIDs)
	if err != nil {
		return nil, fmt.Errorf("wiki: build evidence index: %w", err)
	}

	draft := &Draft{
		PageID:           pageID,
		SourceRevisionID: latestRevision.RevisionID,
		SourcePageIDs:    marshalIDs(sourcePageIDs),
		EvidenceIndex:    marshalEvidenceIndex(evidenceIndex),
		Title:            title,
		Content:          content,
	}
	if err := s.store.InsertDraft(draft); err != nil {
		return nil, err
	}
	return s.store.GetDraft(draft.DraftID)
}

// buildEvidenceIndex unions source_point_ids across sourcePageIDs and
// resolves each to its KP/KU/source location — read-only, generated once at
// draft-creation time (docs/impl/v1/wiki.md 步骤 10).
func (s *Service) buildEvidenceIndex(sourcePageIDs []string) ([]EvidenceIndexEntry, error) {
	seen := make(map[string]bool)
	var pointIDs []string
	for _, pid := range sourcePageIDs {
		p, err := s.store.GetPage(pid)
		if err != nil || p == nil {
			continue
		}
		var ids []string
		json.Unmarshal([]byte(p.SourcePointIDs), &ids)
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				pointIDs = append(pointIDs, id)
			}
		}
	}
	if len(pointIDs) == 0 {
		return nil, nil
	}
	return s.store.PointDetailsForIDs(pointIDs)
}

func marshalEvidenceIndex(entries []EvidenceIndexEntry) string {
	if len(entries) == 0 {
		return "[]"
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// DraftWithStale bundles a Draft with the stale flag GET /wiki/drafts/:id
// needs (docs/impl/v1/wiki.md 步骤 10).
type DraftWithStale struct {
	Draft
	Stale bool
}

// GetDraftWithStale implements docs/impl/v1/wiki.md 步骤 10's stale marking:
// source_revision_id no longer being the page's latest revision.
func (s *Service) GetDraftWithStale(draftID string) (*DraftWithStale, error) {
	d, err := s.store.GetDraft(draftID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	revisions, err := s.store.ListRevisions(d.PageID)
	if err != nil {
		return nil, err
	}
	stale := true
	if len(revisions) > 0 && revisions[len(revisions)-1].RevisionID == d.SourceRevisionID {
		stale = false
	}
	return &DraftWithStale{Draft: *d, Stale: stale}, nil
}

// UpdateDraft implements docs/impl/v1/wiki.md 步骤 10's PATCH /wiki/drafts/:id:
// free-form title/content/note update, no citation whitelist, no section
// structure validation, no point_id extraction — the draft is a human work
// product, not a compile artifact.
func (s *Service) UpdateDraft(draftID string, title, content, note *string) (*Draft, error) {
	existing, err := s.store.GetDraft(draftID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("wiki: draft %s not found", draftID)
	}
	if title != nil {
		existing.Title = *title
	}
	if content != nil {
		existing.Content = *content
	}
	if note != nil {
		existing.Note = *note
	}
	if err := s.store.UpdateDraft(existing); err != nil {
		return nil, err
	}
	return s.store.GetDraft(draftID)
}
