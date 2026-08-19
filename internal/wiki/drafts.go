// Writing drafts (docs/impl/v1/wiki.md 步骤 10): page-derived, freely
// editable writing surfaces. Saving a draft (SyncDraftToPage, 2026-08-19)
// writes its title/content straight onto the source page — no LLM, no
// citation whitelist, just the human's edit taken as-is, with a new
// revision recorded for the audit trail and the page reset to draft status
// pending re-publish.
package wiki

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// SyncDraftToPage (2026-08-19 新增，用户明确要求"保存草稿就自动写回页面"，
// 取代此前"wiki_pages.content 仅由编译产生，没有 draft→page 写回接口"的
// 口径) writes the draft's current title/content directly onto its source
// page — every PATCH /wiki/drafts/:id save (autosave-on-blur or the explicit
// 保存 button) calls this, not just an explicit "publish" action. Unlike
// Compile/Recompile this content never goes through the LLM or the citation
// whitelist — it's a human's free-form edit, taken as-is. What it still does,
// mirroring Recompile: re-derive source_point_ids/source_unit_ids from
// whichever [point_id] tags the edited content actually contains (so
// citation coloring/依赖来源 stay consistent with what's really cited now,
// not what was true before the edit), re-extract "## 摘要" for the summary
// field, insert a new revision (audit trail — nothing is silently
// overwritten), and reset status to draft via ReplaceContent (matches
// Recompile: edited content needs to clear quality gate again before
// (re)publish, even if the page was already published). An archived page
// can't be synced (terminal, same rule as Recompile).
func (s *Service) SyncDraftToPage(draftID string) (*Page, error) {
	draft, err := s.store.GetDraft(draftID)
	if err != nil {
		return nil, err
	}
	if draft == nil {
		return nil, fmt.Errorf("wiki: draft %s not found", draftID)
	}
	page, err := s.store.GetPage(draft.PageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	if page.Status == StatusArchived {
		return nil, ErrPageArchived
	}

	pointIDs := extractPointIDs(draft.Content)
	var sourcePointIDs, sourceUnitIDs []string
	if len(pointIDs) > 0 {
		details, err := s.store.PointDetailsForIDs(pointIDs)
		if err != nil {
			return nil, fmt.Errorf("wiki: sync draft point details: %w", err)
		}
		seenUnit := make(map[string]bool, len(details))
		for _, d := range details {
			sourcePointIDs = append(sourcePointIDs, d.PointID)
			if d.UnitID != "" && !seenUnit[d.UnitID] {
				seenUnit[d.UnitID] = true
				sourceUnitIDs = append(sourceUnitIDs, d.UnitID)
			}
		}
	}
	summary := extractSection(draft.Content, "## 摘要")
	if summary == "" {
		summary = page.Summary
	}

	if err := s.store.ReplaceContent(page.PageID, draft.Title, draft.Content,
		marshalIDs(sourcePointIDs), marshalIDs(sourceUnitIDs), page.SourceLinkIDs, page.ObservedConditions,
		page.Aliases, page.TriggerQuestions, page.UncoveredPoints, page.CompiledFrom, summary, page.Aspects,
		page.PromptVersion, page.ModelName); err != nil {
		return nil, err
	}
	rev := &Revision{PageID: page.PageID, Content: draft.Content, Title: draft.Title, Reason: "draft_sync", DraftID: draftID}
	if err := s.store.InsertRevision(rev); err != nil {
		slog.Error("wiki: insert draft-sync revision failed", "page_id", page.PageID, "error", err)
	} else if err := s.store.UpdateDraftSourceRevision(draftID, rev.RevisionID); err != nil {
		slog.Warn("wiki: update draft source revision failed", "draft_id", draftID, "error", err)
	}
	// 内容已经变成草稿状态、需要重新过质量门才能再发布，正式检索不应该继续
	// 命中旧索引条目（跟 Recompile 的处理一致）。
	if err := s.wikiIndex.Delete(page.PageID); err != nil {
		slog.Warn("wiki: remove page from index after draft sync failed", "page_id", page.PageID, "error", err)
	}

	slog.Info("wiki: synced draft to page", "page_id", page.PageID, "draft_id", draftID)
	return s.store.GetPage(page.PageID)
}

// extractPointIDs is pointIDTagRe applied + deduped, in first-appearance
// order — mirrors the frontend's WIKI_POINT_TAG_RE numbering logic
// (renderWikiContent) so citation order stays consistent between what a
// human sees numbered in the page and what SyncDraftToPage records as
// source_point_ids.
func extractPointIDs(content string) []string {
	matches := pointIDTagRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool, len(matches))
	var ids []string
	for _, m := range matches {
		id := m[1]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
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

// DeleteDraft implements docs/impl/v1/wiki.md 步骤 10's DELETE /wiki/drafts/:id.
// 草稿/修订记录已于 2026-08-19 合并为同一件事（每次保存草稿就是一次新修订），
// 所以删除草稿时要一并删掉它自己写入的全部修订，而不只是删草稿行本身——否
// 则"修订记录"列表里会留下孤儿记录，再也打不开对应的草稿编辑器。
// SyncDraftToPage 每次保存都 INSERT 一条新修订（不是更新已有的），
// wiki_drafts.source_revision_id 只跟踪最新那一条，草稿保存过 N 次就写了 N
// 条修订——早期版本这里只删了 source_revision_id 指向的最后一条，前面几次
// 保存留下的修订全部成了删不掉的孤儿。改用 draft_id（migration 060）批量删
// 除这个草稿写过的所有修订。删除顺序不能反：wiki_drafts.source_revision_id
// 指向该草稿最新写的那条修订（NOT NULL FK），所以必须先删草稿行本身（解除
// 这个依赖），再删它写过的所有修订，否则删除最新那条修订会先撞上这条 FK
// 报错（wiki_revisions.draft_id 反过来指草稿故意不建 FK，就是为了不让两张
// 表互相指着对方导致删除顺序无解，见 migration 060）。草稿从未保存过时没
// 有任何 draft_id 匹配的修订，这一步是空操作，CreateDraft 时派生出的原始
// 页面修订（编译产生，draft_id 为空）不受影响。
func (s *Service) DeleteDraft(draftID string) error {
	draft, err := s.store.GetDraft(draftID)
	if err != nil {
		return err
	}
	if draft == nil {
		return nil
	}
	if err := s.store.DeleteDraft(draftID); err != nil {
		return err
	}
	if err := s.store.DeleteRevisionsByDraft(draftID); err != nil {
		return err
	}
	// 兼容 migration 060 之前写下的、没有 draft_id 的历史 draft_sync 记录（见
	// DeleteLegacyDraftSyncRevisions 注释）——没有这一步，2026-08-19 早些时候
	// 用户实测过的那批草稿（当时代码还没打 draft_id 标签）删除草稿后会有一批
	// 修订永久留在列表里删不掉。
	return s.store.DeleteLegacyDraftSyncRevisions(draft.PageID)
}

// UpdateDraft implements docs/impl/v1/wiki.md 步骤 10's PATCH /wiki/drafts/:id:
// free-form title/content/note update on the draft itself, no citation
// whitelist, no section structure validation on the draft row — but every
// save where title or content actually changed (note-only edits don't
// count) also calls SyncDraftToPage to write straight through to the source
// page (2026-08-19: every save writes back, not a separate explicit publish
// step). A sync failure is logged and swallowed rather than failing the
// whole request — the draft itself did save correctly, and losing that on a
// transient sync error would be worse than a page that's one save behind.
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
	if title != nil || content != nil {
		if _, err := s.SyncDraftToPage(draftID); err != nil {
			slog.Error("wiki: sync draft to page failed", "draft_id", draftID, "error", err)
		}
	}
	return s.store.GetDraft(draftID)
}
