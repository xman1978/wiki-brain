package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// publishEntryPage inserts+publishes a concept page directly (bypassing the
// LLM compile pipeline, which is already covered elsewhere) so relation/
// topic-candidate tests can set up multiple published pages cheaply.
func publishEntryPage(t *testing.T, svc *Service, pageID, conceptID, title string, pointIDs []string) *Page {
	t.Helper()
	p := &Page{
		PageID:         pageID,
		PageType:       PageTypeConcept,
		Title:          title,
		Content:        "## 稳定结论\n内容\n\n## 展开说明\n说明\n\n## 待验证点\n无\n\n## 依赖来源\n无\n",
		Status:         StatusDraft,
		SourcePointIDs: marshalIDs(pointIDs),
		PromptVersion:  "v1",
		ModelName:      "test",
	}
	p.EntryID = nullableString(conceptID)
	if err := svc.store.InsertPage(p); err != nil {
		t.Fatalf("insert page %s: %v", pageID, err)
	}
	if err := svc.store.InsertRevision(&Revision{PageID: pageID, Content: p.Content, Reason: "seed"}); err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	got, err := svc.Publish(pageID)
	if err != nil {
		t.Fatalf("publish %s: %v", pageID, err)
	}
	return got
}

func seedKPRelation(t *testing.T, db *sql.DB, relationID, sourcePointID, targetPointID, relationType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_point_relations
		(relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope)
		VALUES (?, ?, ?, ?, 'bidirectional', 'v1', 'cross')`,
		relationID, sourcePointID, targetPointID, relationType); err != nil {
		t.Fatalf("seed kp relation: %v", err)
	}
}

// TestPageRelations_DerivedFromKPN implements docs/impl/v1/wiki.md 完成标准
// 两层架构扩展: "两个概念页的 KP 之间存在 KPN related/contradicts 关系时，
// publish 后自动派生出对应的 wiki_page_relations 行，方向归一化后无重复行"。
func TestPageRelations_DerivedFromKPN(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.RelationKPNMin = 1
	cfg.RelationSharedPointMin = 100 // disable the shared-point path so only the KPN path is under test
	svc.cfg = cfg

	seedDomain(t, db, "d2", "Domain Two")
	seedEntry(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	publishEntryPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	seedKPRelation(t, db, "rel1", "p1", "p3", "related")
	publishEntryPage(t, svc, "page2", "c2", "Page Two", []string{"p3"})

	rels, err := svc.store.ListPageRelations("page1")
	if err != nil {
		t.Fatalf("list page relations: %v", err)
	}
	var related []PageRelation
	for _, r := range rels {
		if r.RelationType == RelationRelated {
			related = append(related, r)
		}
	}
	if len(related) != 1 {
		t.Fatalf("expected exactly 1 related relation, got %d: %+v", len(related), related)
	}
	r := related[0]
	if r.DerivedFrom != DerivedFromKPN {
		t.Errorf("expected derived_from=kpn, got %s", r.DerivedFrom)
	}
	// Normalized dictionary order: from < to.
	if r.FromPageID >= r.ToPageID {
		t.Errorf("expected from_page_id < to_page_id (normalized), got from=%s to=%s", r.FromPageID, r.ToPageID)
	}

	// Same relation viewed from page2's side must be the same row, not duplicated.
	rels2, err := svc.store.ListPageRelations("page2")
	if err != nil {
		t.Fatalf("list page relations page2: %v", err)
	}
	count2 := 0
	for _, r := range rels2 {
		if r.RelationType == RelationRelated {
			count2++
		}
	}
	if count2 != 1 {
		t.Errorf("expected page2 to see exactly 1 related relation, got %d", count2)
	}
}

// indexPointForSearch puts a minimal current-lifecycle doc into the points
// Bleve index so topic-candidate range retrieval (DetectTopicCandidate 步骤
// 3, a bleve search over pointsIndex) can find it — production indexing is
// unit.Service's job; tests exercising the wiki-side consumer seed the index
// directly.
func indexPointForSearch(t *testing.T, idx bleve.Index, pointID, content string) {
	t.Helper()
	if err := idx.Index(pointID, map[string]interface{}{
		"point_id": pointID, "content": content, "lifecycle": "current",
	}); err != nil {
		t.Fatalf("index point %s for search: %v", pointID, err)
	}
}

// TestDetectTopicCandidate_QuadrupleClusterAndSecondTierCompile implements
// docs/impl/v1/wiki.md 完成标准 两层架构扩展 (2026-08-03 修订): candidate-range
// retrieval, qualifying filter, concept grouping, second-tier admission,
// contains wiring, and second-tier compile producing the five required
// sections with member_roles and a citation whitelist confined to the
// members' source_point_ids union.
func TestDetectTopicCandidate_QuadrupleClusterAndSecondTierCompile(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicMemberMin = 3
	cfg.RelationSharedPointMin = 1 // let shared-point-count derive "related" without needing KPN rows
	cfg.TopicCandidateKPMax = 50
	cfg.TopicReliabilityMin = 0 // this test's focus is grouping/compile, not reliability tuning
	svc.cfg = cfg

	seedDomain(t, db, "d2", "Domain Two")
	seedEntry(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	seedEntry(t, db, "c3", "d2", "Concept Three")
	seedKU(t, db, "u4", "s1", "c3", "Topic D", 6, 10)
	seedKP(t, db, "p4", "u4", "s1", "point four content")
	seedVerifiedLink(t, db, "link-p4", "p4")

	// Three concept pages, chained by a shared point: page1(p1,p2) -
	// page2(p2,p3) - page3(p3,p4). The candidate-range search below returns
	// all four points, grouping into three entries (c1/c2/c3), each already
	// published.
	publishEntryPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	publishEntryPage(t, svc, "page2", "c2", "Page Two", []string{"p2", "p3"})
	publishEntryPage(t, svc, "page3", "c3", "Page Three", []string{"p3", "p4"})

	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		indexPointForSearch(t, svc.pointsIndex, id, "测试主题相关内容 "+id)
	}

	cand, underfilled, err := svc.DetectTopicCandidate("测试主题", "了解", "", "", 3, 7, 1)
	if err != nil {
		t.Fatalf("detect topic candidate: %v", err)
	}
	if underfilled != nil {
		t.Fatalf("expected a candidate, got underfilled signal: %+v", underfilled)
	}
	if cand == nil {
		t.Fatal("expected a topic candidate, got nil")
	}
	shellID := cand.PageID

	members, err := svc.store.ContainsMembers(shellID)
	if err != nil {
		t.Fatalf("contains members: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 contains members, got %d: %v", len(members), members)
	}

	// Members (page1/page2/page3) are already published via
	// publishEntryPage above, satisfying the topic compile precondition.

	compileOutput := `{
		"title": "主题：一二三",
		"content": "## 主题概览\n概览\n\n## 主线结论\n[p1] 结论一\n\n## 子主题分工\n分工说明\n\n## 跨主题矛盾与待验证点\n无\n\n## 依赖页面\n三个成员页面\n",
		"cited_point_ids": ["p1"],
		"aliases": [],
		"trigger_questions": [],
		"member_roles": [
			{"member_page_id": "` + members[0] + `", "aspect": "基础", "question_types": ["定义"]}
		]
	}`
	fake.SetResponse("wiki_topic_analyze.md", llm.FakeResponse{Output: `{"claims":[{"summary":"跨成员主线结论","cited_point_ids":["p1"]}],"tensions":[]}`})
	fake.SetResponse("wiki_topic_compile.md", llm.FakeResponse{Output: compileOutput})

	page, err := svc.CompileTopic(context.Background(), shellID, nil, nil)
	if err != nil {
		t.Fatalf("compile topic: %v", err)
	}
	if !hasRequiredTopicSections(page.Content) {
		t.Errorf("compiled topic content missing required sections: %s", page.Content)
	}

	var memberRoles []MemberRole
	json.Unmarshal([]byte(page.MemberRoles), &memberRoles)
	if len(memberRoles) != 1 || memberRoles[0].MemberPageID != members[0] {
		t.Errorf("expected member_roles to persist the one valid entry, got %+v", memberRoles)
	}

	var sourcePointIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	for _, id := range sourcePointIDs {
		if id != "p1" {
			t.Errorf("unexpected cited point_id %s outside members' union", id)
		}
	}
}

// TestTopicPage_NeverDirectlyAnswered implements docs/impl/v1/wiki.md 完成标准:
// "主题页命中后不产生 answer_wiki 调用，而是展开成员概念页进候选" — asserted
// as: gatherDirectAnswerCandidates never returns a topic page id, so the
// number of answerFromPage (answer_wiki) attempts is bounded by the expanded
// concept page count, never by 1 extra for the topic page itself.
func TestTopicPage_NeverDirectlyAnswered(t *testing.T) {
	svc, fake, db, wikiIdx := setupTestService(t)
	_ = db

	// Build a topic shell page containing page1 (concept, published) as its
	// only searchable member so we don't need a full connected-component
	// detection pass for this test.
	page1 := publishEntryPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})

	shell := &Page{PageID: "topic1", PageType: PageTypeTopic, Title: "Topic Shell", Content: "", Status: StatusDraft}
	if err := svc.store.InsertPage(shell); err != nil {
		t.Fatalf("insert shell: %v", err)
	}
	if err := svc.store.UpsertPageRelation("topic1", "page1", RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatalf("upsert contains: %v", err)
	}
	// Index the shell so the lexical entry can hit it.
	if err := wikiIdx.Index("topic1", map[string]interface{}{
		"page_id": "topic1", "title": "Topic Shell", "content": "关于测试主题的概览", "status": StatusPublished,
	}); err != nil {
		t.Fatalf("index shell: %v", err)
	}
	if err := svc.store.UpdatePageStatus("topic1", StatusPublished); err != nil {
		t.Fatalf("publish shell status: %v", err)
	}

	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答内容","citations":["p1"],"sufficient":true}`})

	candidates, skeleton, err := svc.gatherDirectAnswerCandidates("关于测试主题的概览", "", "", "", "", 0, 3)
	if err != nil {
		t.Fatalf("gather candidates: %v", err)
	}
	for _, c := range candidates {
		if c == "topic1" {
			t.Fatalf("topic page must never appear in the direct-answer candidate list, got %v", candidates)
		}
	}
	if skeleton == nil || skeleton.PageID != "topic1" {
		t.Fatalf("expected skeleton info for topic1, got %+v", skeleton)
	}
	found := false
	for _, m := range skeleton.Members {
		if m.PageID == page1.PageID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skeleton members to include %s, got %+v", page1.PageID, skeleton.Members)
	}

	// answer_wiki call count is bounded by len(candidates) (concept pages
	// only) — the topic page itself never consumes a call.
	result, ok, _, err := svc.TryDirectAnswer(context.Background(), "关于测试主题的概览", "", "", "", "", 0, 3)
	if err != nil {
		t.Fatalf("try direct answer: %v", err)
	}
	answerWikiCalls := 0
	for _, c := range fake.Calls() {
		if c.PromptFile == "answer_wiki.md" {
			answerWikiCalls++
		}
	}
	if answerWikiCalls > len(candidates) {
		t.Errorf("answer_wiki calls (%d) exceeded expanded concept candidate count (%d)", answerWikiCalls, len(candidates))
	}
	if !ok || result == nil || result.PageID != "page1" {
		t.Errorf("expected direct answer to succeed via expanded member page1, got result=%+v ok=%v", result, ok)
	}
}

// TestDraft_NeverWritesBackToPage implements docs/impl/v1/wiki.md 完成标准:
// "代码中不存在 draft → page 的写回路径" — behaviorally asserted: editing a
// draft's content never changes the page it was derived from.
func TestDraft_NeverWritesBackToPage(t *testing.T) {
	svc, _, _, _ := setupTestService(t)
	page1 := publishEntryPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	originalContent := page1.Content

	draft, err := svc.CreateDraft("page1", DraftModePage)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	newTitle := "改写后的标题"
	newContent := "完全不同的草稿正文，人工随意改写"
	if _, err := svc.UpdateDraft(draft.DraftID, &newTitle, &newContent, nil); err != nil {
		t.Fatalf("update draft: %v", err)
	}

	reloaded, err := svc.store.GetPage("page1")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if reloaded.Content != originalContent {
		t.Errorf("page content changed after draft edit — draft -> page write-back detected!\nwant: %q\ngot:  %q", originalContent, reloaded.Content)
	}
	if reloaded.Title != page1.Title {
		t.Errorf("page title changed after draft edit — draft -> page write-back detected!")
	}
}

// TestCreateTopicManual_BuildsShellFromScopeSearch covers docs/impl/v1/wiki.md
// 步骤 8 "人工手动指定主题" (2026-08-03 修订): the request gives a topic
// scope (name/description), not a member-page list; the shell is built from
// whatever qualifying entries the scope search turns up that already have a
// published page. compiled_from records manual_trigger; readiness is
// informational, never a gate.
func TestCreateTopicManual_BuildsShellFromScopeSearch(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicMemberMin = 3
	cfg.TopicCandidateKPMax = 50
	svc.cfg = cfg

	seedDomain(t, db, "d2", "Domain Two")
	seedEntry(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	seedEntry(t, db, "c3", "d2", "Concept Three")
	seedKU(t, db, "u4", "s1", "c3", "Topic D", 6, 10)
	seedKP(t, db, "p4", "u4", "s1", "point four content")
	seedVerifiedLink(t, db, "link-p4", "p4")

	publishEntryPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	publishEntryPage(t, svc, "page2", "c2", "Page Two", []string{"p3"})
	publishEntryPage(t, svc, "page3", "c3", "Page Three", []string{"p4"})

	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		indexPointForSearch(t, svc.pointsIndex, id, "手动主题测试内容 "+id)
	}

	if _, _, err := svc.CreateTopicManual(context.Background(), "", "空主题名不允许", ""); err == nil {
		t.Fatal("expected error for empty topic_name")
	}

	cand, readiness, err := svc.CreateTopicManual(context.Background(), "手动主题测试", "范围描述", "")
	if err != nil {
		t.Fatalf("CreateTopicManual: %v", err)
	}
	if readiness == nil || readiness.MemberCount != 3 {
		t.Fatalf("expected readiness.member_count=3, got %+v", readiness)
	}

	page, err := svc.store.GetPage(cand.PageID)
	if err != nil || page == nil {
		t.Fatalf("get shell: %v", err)
	}
	if page.PageType != PageTypeTopic || page.Status != StatusDraft {
		t.Errorf("expected topic/draft shell, got type=%s status=%s", page.PageType, page.Status)
	}
	var from []string
	if err := json.Unmarshal([]byte(page.CompiledFrom), &from); err != nil {
		t.Fatalf("parse compiled_from: %v", err)
	}
	if len(from) != 1 || from[0] != ManualTriggerSentinel {
		t.Errorf("expected compiled_from=[%q], got %v", ManualTriggerSentinel, from)
	}
	members, err := svc.store.ContainsMembers(cand.PageID)
	if err != nil {
		t.Fatalf("contains: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 members (one per published concept), got %v", members)
	}
}

// TestPreviewTopicCandidates_ReadOnlyAndShowsReadiness locks 分步向导步骤 1
// (docs/impl/v1/wiki.md 步骤 8): PreviewTopicCandidates must report per-entry
// readiness/publication state without writing anything (no wiki_candidate
// learning_result, no shell page) — unlike CreateTopicManual, which silently
// drops unready entries into uncovered_entries.
func TestPreviewTopicCandidates_ReadOnlyAndShowsReadiness(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicCandidateKPMax = 50
	svc.cfg = cfg

	// c1 is already published by setupTestService's baseline data (page1-style
	// fixtures) — reuse it as the "already published" case; add a fresh,
	// never-published, never-verified entry as the "unready" case.
	seedDomain(t, db, "d3", "Domain Three")
	seedEntry(t, db, "c-unready", "d3", "Unready Concept")
	seedKU(t, db, "u-unready", "s1", "c-unready", "Topic E", 1, 5)
	seedKP(t, db, "p-unready", "u-unready", "s1", "unready point content")

	publishEntryPage(t, svc, "page-preview", "c1", "Preview Page", []string{"p1"})

	for _, id := range []string{"p1", "p-unready"} {
		indexPointForSearch(t, svc.pointsIndex, id, "预览向导测试内容 "+id)
	}

	entries, err := svc.PreviewTopicCandidates(context.Background(), "预览向导测试", "范围描述", "")
	if err != nil {
		t.Fatalf("PreviewTopicCandidates: %v", err)
	}

	byEntry := make(map[string]TopicCandidateEntry, len(entries))
	for _, e := range entries {
		byEntry[e.EntryID] = e
	}
	published, ok := byEntry["c1"]
	if !ok {
		t.Fatalf("expected c1 in preview entries, got %+v", entries)
	}
	if published.AlreadyPublishedPageID != "page-preview" || !published.IsReady {
		t.Errorf("expected c1 to report already-published page and is_ready=true, got %+v", published)
	}

	unready, ok := byEntry["c-unready"]
	if !ok {
		t.Fatalf("expected c-unready in preview entries, got %+v", entries)
	}
	if unready.AlreadyPublishedPageID != "" || unready.IsReady {
		t.Errorf("expected c-unready to be unpublished and not ready, got %+v", unready)
	}
	if unready.QualifyingKPCount != 1 {
		t.Errorf("expected c-unready qualifying_kp_count=1, got %d", unready.QualifyingKPCount)
	}

	pending, err := svc.store.HasPendingWikiCandidate("c-unready")
	if err != nil {
		t.Fatalf("HasPendingWikiCandidate: %v", err)
	}
	if pending {
		t.Error("PreviewTopicCandidates must not write a wiki_candidate learning_result (read-only)")
	}
}

// waitWizardTaskStatus polls GetWizardTaskDetail until status leaves
// candidates_loading or the timeout fires — the retrieval runs in a
// background goroutine (docs/impl/v1/wiki.md 步骤 8 "分步向导" 断点续开).
func waitWizardTaskStatus(t *testing.T, svc *Service, taskID string) *WizardTaskDetail {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := svc.GetWizardTaskDetail(taskID)
		if err != nil {
			t.Fatalf("GetWizardTaskDetail: %v", err)
		}
		if detail == nil {
			t.Fatal("wizard task disappeared while waiting")
		}
		if detail.Status != WizardTaskStatusCandidatesLoading {
			return detail
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for wizard task to leave candidates_loading")
	return nil
}

// TestStartWizardTask_ResumesExistingActiveTask locks 步骤 8 "分步向导"
// 断点续开: domain_id is UNIQUE, so a second StartWizardTask call for the
// same domain must return the existing task rather than starting a second
// retrieval (which would also just fail the UNIQUE constraint).
func TestStartWizardTask_ResumesExistingActiveTask(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedDomain(t, db, "d-wizard", "Domain Wizard")

	first, err := svc.StartWizardTask("向导续开测试", "", "d-wizard")
	if err != nil {
		t.Fatalf("StartWizardTask: %v", err)
	}
	second, err := svc.StartWizardTask("向导续开测试（换一个名字也一样）", "", "d-wizard")
	if err != nil {
		t.Fatalf("StartWizardTask (resume): %v", err)
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("expected resuming the same task, got first=%s second=%s", first.TaskID, second.TaskID)
	}
	waitWizardTaskStatus(t, svc, first.TaskID)
}

// TestWizardTask_DetailRefreshesLiveStateAndDeleteFreesDomainSlot locks two
// things in one flow: GetWizardTaskDetail must re-derive publish state live
// (not serve the retrieval-time snapshot) once a candidate is compiled +
// published elsewhere, and DeleteWizardTask must free the domain_id slot for
// a fresh StartWizardTask.
func TestWizardTask_DetailRefreshesLiveStateAndDeleteFreesDomainSlot(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedDomain(t, db, "d-wizard2", "Domain Wizard Two")
	seedEntry(t, db, "c-wizard", "d-wizard2", "Wizard Concept")
	seedKU(t, db, "u-wizard", "s1", "c-wizard", "Topic Wizard", 1, 5)
	seedKP(t, db, "p-wizard", "u-wizard", "s1", "wizard task test content")
	indexPointForSearch(t, svc.pointsIndex, "p-wizard", "向导任务刷新测试内容 p-wizard")

	task, err := svc.StartWizardTask("向导任务刷新测试", "", "d-wizard2")
	if err != nil {
		t.Fatalf("StartWizardTask: %v", err)
	}
	detail := waitWizardTaskStatus(t, svc, task.TaskID)
	if detail.Status != WizardTaskStatusCandidatesReady {
		t.Fatalf("expected candidates_ready, got status=%s error=%s", detail.Status, detail.ErrorMessage)
	}
	found := false
	for _, e := range detail.Entries {
		if e.EntryID == "c-wizard" {
			found = true
			if e.AlreadyPublishedPageID != "" {
				t.Errorf("expected c-wizard not yet published, got %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("expected c-wizard among wizard task entries, got %+v", detail.Entries)
	}

	// Compile + publish c-wizard out of band (as the wizard's "编译"/"发布"
	// buttons would), then confirm the next GetWizardTaskDetail call reports
	// it as published without having re-run the expensive retrieval.
	publishEntryPage(t, svc, "page-wizard", "c-wizard", "Wizard Page", []string{"p-wizard"})
	refreshed, err := svc.GetWizardTaskDetail(task.TaskID)
	if err != nil {
		t.Fatalf("GetWizardTaskDetail (refresh): %v", err)
	}
	refreshedFound := false
	for _, e := range refreshed.Entries {
		if e.EntryID == "c-wizard" {
			refreshedFound = true
			if e.AlreadyPublishedPageID != "page-wizard" {
				t.Errorf("expected live-refreshed already_published_page_id=page-wizard, got %+v", e)
			}
		}
	}
	if !refreshedFound {
		t.Fatalf("expected c-wizard among refreshed entries, got %+v", refreshed.Entries)
	}

	if err := svc.DeleteWizardTask(task.TaskID); err != nil {
		t.Fatalf("DeleteWizardTask: %v", err)
	}
	if gone, err := svc.GetWizardTaskDetail(task.TaskID); err != nil || gone != nil {
		t.Fatalf("expected task gone after delete, got detail=%+v err=%v", gone, err)
	}

	again, err := svc.StartWizardTask("新任务", "", "d-wizard2")
	if err != nil {
		t.Fatalf("StartWizardTask after delete: %v", err)
	}
	if again.TaskID == task.TaskID {
		t.Fatal("expected a fresh task after delete, got the same task_id")
	}
	waitWizardTaskStatus(t, svc, again.TaskID)
}

// TestOutlineRecallCandidates_FindsCandidateFullTextMisses locks 步骤 8
// 候选检索 1b (2026-08-07 修订): a KP whose content wouldn't full-text-match
// the topic query, but whose source outline heading does, must still surface
// via the outline-recall branch.
func TestOutlineRecallCandidates_FindsCandidateFullTextMisses(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicCandidateKPMax = 50
	svc.cfg = cfg

	seedDomain(t, db, "d-outline", "Domain Outline")
	seedEntry(t, db, "c-outline", "d-outline", "Outline Concept")
	if _, err := db.Exec(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, summary, line_start, line_end, node_type, position)
		VALUES ('o1', 's1', NULL, 1, '达梦数据库性能调优章节', '', 1, 5, 'structural', 0)`); err != nil {
		t.Fatalf("seed outline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, entry_id, outline_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u-outline', 's1', 'c-outline', 'o1', 'Topic Outline', 1, 5, 'completed', 'v1')`); err != nil {
		t.Fatalf("seed KU with outline_id: %v", err)
	}
	seedKP(t, db, "p-outline", "u-outline", "s1", "跟主题查询词完全不重合的内容")
	// Deliberately NOT indexed into svc.pointsIndex — only reachable via
	// outline recall, not the full-text branch.
	if err := svc.outlinesIndex.Index("o1", map[string]interface{}{
		"outline_id": "o1", "source_id": "s1", "title": "达梦数据库性能调优章节", "summary": "", "level": 1, "node_type": "structural",
	}); err != nil {
		t.Fatalf("index outline: %v", err)
	}

	entries, err := svc.PreviewTopicCandidates(context.Background(), "达梦数据库性能调优", "", "")
	if err != nil {
		t.Fatalf("PreviewTopicCandidates: %v", err)
	}
	for _, e := range entries {
		if e.EntryID == "c-outline" {
			return
		}
	}
	t.Fatalf("expected c-outline (reachable only via outline recall) in preview entries, got %+v", entries)
}

// TestJudgeTopicCandidateRelevance_FiltersIrrelevant locks 步骤 8 候选检索
// 1b's LLM relevance judge: a candidate the model marks relevant=false must
// be dropped before grouping by entry_id.
func TestJudgeTopicCandidateRelevance_FiltersIrrelevant(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicCandidateKPMax = 50
	svc.cfg = cfg

	seedDomain(t, db, "d-relevance", "Domain Relevance")
	seedEntry(t, db, "c-irrelevant", "d-relevance", "Irrelevant Concept")
	seedKU(t, db, "u-irrelevant", "s1", "c-irrelevant", "Topic Irrelevant", 1, 5)
	seedKP(t, db, "p-irrelevant", "u-irrelevant", "s1", "irrelevant point content")

	for _, id := range []string{"p1", "p-irrelevant"} {
		indexPointForSearch(t, svc.pointsIndex, id, "相关性判定测试内容 "+id)
	}

	fake.SetResponse("wiki_topic_candidate_rerank.md", llm.FakeResponse{Output: `{
		"results": [
			{"candidate_id": "p1", "relevant": true, "reason": "属于该主题"},
			{"candidate_id": "p-irrelevant", "relevant": false, "reason": "只是词面相关，实际属于另一个主题"}
		]
	}`})

	entries, err := svc.PreviewTopicCandidates(context.Background(), "相关性判定测试", "", "")
	if err != nil {
		t.Fatalf("PreviewTopicCandidates: %v", err)
	}
	for _, e := range entries {
		if e.EntryID == "c-irrelevant" {
			t.Fatalf("expected c-irrelevant to be filtered out by relevance judge, got %+v", entries)
		}
	}
	found := false
	for _, e := range entries {
		if e.EntryID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected c1 (relevant=true) to remain, got %+v", entries)
	}
}

// TestJudgeTopicCandidateRelevance_FailOpenOnLLMError locks the fail-open
// contract: when the relevance-judge LLM call errors, candidates are kept
// unfiltered rather than the whole preview failing or silently emptying.
func TestJudgeTopicCandidateRelevance_FailOpenOnLLMError(t *testing.T) {
	svc, fake, _, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicCandidateKPMax = 50
	svc.cfg = cfg

	fake.SetResponse("wiki_topic_candidate_rerank.md", llm.FakeResponse{Err: fmt.Errorf("simulated LLM outage")})

	for _, id := range []string{"p1", "p2"} {
		indexPointForSearch(t, svc.pointsIndex, id, "失败开放测试内容 "+id)
	}

	entries, err := svc.PreviewTopicCandidates(context.Background(), "失败开放测试", "", "")
	if err != nil {
		t.Fatalf("PreviewTopicCandidates: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.EntryID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected c1 kept despite relevance-judge LLM failure (fail-open), got %+v", entries)
	}
}

// TestCreateTopicFromMembers_ExplicitList locks 分步向导步骤 3
// (docs/impl/v1/wiki.md 步骤 8): membership comes directly from the caller,
// not from isEntryReady — and non-published / topic-type members are
// rejected via ErrMembersNotPublished.
func TestCreateTopicFromMembers_ExplicitList(t *testing.T) {
	svc, _, db, _ := setupTestService(t)

	seedDomain(t, db, "d4", "Domain Four")
	seedEntry(t, db, "c5", "d4", "Concept Five")
	seedKU(t, db, "u5", "s1", "c5", "Topic F", 1, 5)
	seedKP(t, db, "p5", "u5", "s1", "point five content")
	seedVerifiedLink(t, db, "link-p5", "p5")

	page1 := publishEntryPage(t, svc, "page-explicit-1", "c1", "Explicit Page One", []string{"p1"})
	page2 := publishEntryPage(t, svc, "page-explicit-2", "c5", "Explicit Page Two", []string{"p5"})

	if _, err := svc.CreateTopicFromMembers("显式成员主题", nil); err == nil {
		t.Fatal("expected error for empty member_page_ids")
	}

	draftPage := &Page{
		PageID: "draft-not-published", PageType: PageTypeConcept, Title: "Draft",
		Content: "", Status: StatusDraft,
	}
	if err := svc.store.InsertPage(draftPage); err != nil {
		t.Fatalf("insert draft page: %v", err)
	}
	_, err := svc.CreateTopicFromMembers("显式成员主题", []string{page1.PageID, draftPage.PageID})
	var notPublished *ErrMembersNotPublished
	if !errors.As(err, &notPublished) {
		t.Fatalf("expected ErrMembersNotPublished for unpublished member, got %v", err)
	}

	cand, err := svc.CreateTopicFromMembers("显式成员主题", []string{page1.PageID, page2.PageID})
	if err != nil {
		t.Fatalf("CreateTopicFromMembers: %v", err)
	}
	page, err := svc.store.GetPage(cand.PageID)
	if err != nil || page == nil {
		t.Fatalf("get shell: %v", err)
	}
	if page.PageType != PageTypeTopic || page.Status != StatusDraft {
		t.Errorf("expected topic/draft shell, got type=%s status=%s", page.PageType, page.Status)
	}
	members, err := svc.store.ContainsMembers(cand.PageID)
	if err != nil {
		t.Fatalf("contains: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 explicit members, got %v", members)
	}
}

// TestQualifyingPointsByIDs_DoesNotRequireVerified locks docs/impl/v1/wiki.md
// 步骤 8 第 4 步 (2026-08-04): topic-scope material only needs lifecycle=current
// (+ non-null entry_id). Verified ActivationLink stays a first-tier /
// reliability / publish concern, not a topic-scope inclusion gate.
func TestQualifyingPointsByIDs_DoesNotRequireVerified(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedKP(t, db, "p-unver", "u1", "s1", "unverified current kp")
	// p1 already has a verified link from setupTestService; p-unver has none.

	got, err := svc.store.QualifyingPointsByIDs([]string{"p1", "p-unver", "missing"})
	if err != nil {
		t.Fatalf("QualifyingPointsByIDs: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.PointID] = true
	}
	if !ids["p1"] || !ids["p-unver"] {
		t.Fatalf("expected both current KPs (with and without verified link), got %v", got)
	}
	if ids["missing"] {
		t.Fatal("unexpected missing point_id in result")
	}
}

