package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// publishConceptPage inserts+publishes a concept page directly (bypassing the
// LLM compile pipeline, which is already covered elsewhere) so relation/
// topic-candidate tests can set up multiple published pages cheaply.
func publishConceptPage(t *testing.T, svc *Service, pageID, conceptID, title string, pointIDs []string) *Page {
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
	p.ConceptID = nullableString(conceptID)
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
	seedConcept(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	publishConceptPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	seedKPRelation(t, db, "rel1", "p1", "p3", "related")
	publishConceptPage(t, svc, "page2", "c2", "Page Two", []string{"p3"})

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

// TestTopicCandidates_ConnectedComponentAndSecondTierCompile implements
// docs/impl/v1/wiki.md 完成标准 两层架构扩展: connected-component detection,
// contains wiring, and second-tier compile producing the five required
// sections with member_roles and a citation whitelist confined to the
// members' source_point_ids union.
func TestTopicCandidates_ConnectedComponentAndSecondTierCompile(t *testing.T) {
	svc, fake, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicMemberMin = 3
	cfg.TopicMemberMax = 8
	cfg.RelationSharedPointMin = 1 // let shared-point-count derive "related" without needing KPN rows
	svc.cfg = cfg

	seedDomain(t, db, "d2", "Domain Two")
	seedConcept(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	seedConcept(t, db, "c3", "d2", "Concept Three")
	seedKU(t, db, "u4", "s1", "c3", "Topic D", 6, 10)
	seedKP(t, db, "p4", "u4", "s1", "point four content")
	seedVerifiedLink(t, db, "link-p4", "p4")

	// Three concept pages, chained by a shared point so all three land in one
	// connected component: page1(p1,p2) - page2(p2,p3) - page3(p3,p4).
	publishConceptPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	publishConceptPage(t, svc, "page2", "c2", "Page Two", []string{"p2", "p3"})
	publishConceptPage(t, svc, "page3", "c3", "Page Three", []string{"p3", "p4"})

	candidates, oversized, err := svc.DetectTopicCandidates()
	if err != nil {
		t.Fatalf("detect topic candidates: %v", err)
	}
	if len(oversized) != 0 {
		t.Errorf("expected no oversized clusters, got %+v", oversized)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 topic candidate, got %d: %+v", len(candidates), candidates)
	}
	shellID := candidates[0].PageID

	members, err := svc.store.ContainsMembers(shellID)
	if err != nil {
		t.Fatalf("contains members: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 contains members, got %d: %v", len(members), members)
	}

	// Members (page1/page2/page3) are already published via
	// publishConceptPage above, satisfying the topic compile precondition.

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
	page1 := publishConceptPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})

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
	page1 := publishConceptPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
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

// TestCreateTopicManual_BuildsShellWithManualTrigger covers docs/impl/v1/wiki.md
// 步骤 8 "人工指定成员手动创建主题页": hard gates on member count + published
// concept pages; Study coherence is informational; compiled_from records
// manual_trigger.
func TestCreateTopicManual_BuildsShellWithManualTrigger(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	cfg := svc.cfg
	cfg.TopicMemberMin = 3
	cfg.TopicMemberMax = 8
	svc.cfg = cfg

	seedDomain(t, db, "d2", "Domain Two")
	seedConcept(t, db, "c2", "d2", "Concept Two")
	seedKU(t, db, "u3", "s1", "c2", "Topic C", 1, 5)
	seedKP(t, db, "p3", "u3", "s1", "point three content")
	seedVerifiedLink(t, db, "link-p3", "p3")

	seedConcept(t, db, "c3", "d2", "Concept Three")
	seedKU(t, db, "u4", "s1", "c3", "Topic D", 6, 10)
	seedKP(t, db, "p4", "u4", "s1", "point four content")
	seedVerifiedLink(t, db, "link-p4", "p4")

	publishConceptPage(t, svc, "page1", "c1", "Page One", []string{"p1", "p2"})
	publishConceptPage(t, svc, "page2", "c2", "Page Two", []string{"p3"})
	publishConceptPage(t, svc, "page3", "c3", "Page Three", []string{"p4"})

	if _, _, err := svc.CreateTopicManual([]string{"page1", "page2"}); err == nil {
		t.Fatal("expected error for too-few members")
	}

	cand, readiness, err := svc.CreateTopicManual([]string{"page1", "page2", "page3", "page2"})
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
		t.Fatalf("expected 3 members after dedupe, got %v", members)
	}

	draftPage := &Page{
		PageID: "page-draft", PageType: PageTypeConcept, Title: "Draft",
		Content: "x", Status: StatusDraft, SourcePointIDs: `["p1"]`,
		PromptVersion: "v1", ModelName: "test",
	}
	draftPage.ConceptID = nullableString("c1")
	if err := svc.store.InsertPage(draftPage); err != nil {
		t.Fatalf("insert draft concept page: %v", err)
	}
	if _, _, err := svc.CreateTopicManual([]string{"page1", "page2", "page-draft"}); err == nil {
		t.Fatal("expected error when a member is not published")
	}
}

