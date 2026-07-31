package wiki

import "testing"

// TestListTopicPages_IncludesShellsAndMemberCount covers the 知识地图 rail's
// data source: every topic-type page (draft shell or compiled) with its live
// contains-member count.
func TestListTopicPages_IncludesShellsAndMemberCount(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedConcept(t, db, "c2", "d1", "Concept Two")

	p1 := publishConceptPage(t, svc, "p1", "c1", "概念一", []string{"pt1"})
	p2 := publishConceptPage(t, svc, "p2", "c2", "概念二", []string{"pt2"})
	_ = p2

	shell := &Page{PageID: "topic1", PageType: PageTypeTopic, Title: "壳页", Content: "", Status: StatusDraft}
	if err := svc.store.InsertPage(shell); err != nil {
		t.Fatalf("insert topic shell: %v", err)
	}
	if err := svc.store.UpsertPageRelation("topic1", p1.PageID, RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatalf("insert contains: %v", err)
	}

	topics, err := svc.store.ListTopicPages()
	if err != nil {
		t.Fatalf("list topic pages: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic page, got %d", len(topics))
	}
	if topics[0].PageID != "topic1" || topics[0].MemberCount != 1 {
		t.Errorf("unexpected topic summary: %+v", topics[0])
	}
	if topics[0].Content != "" || topics[0].Status != StatusDraft {
		t.Errorf("expected an uncompiled shell (empty content, draft status), got content=%q status=%q", topics[0].Content, topics[0].Status)
	}
}

func TestListTopicMemberPages_ReturnsFullPageRows(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedConcept(t, db, "c2", "d1", "Concept Two")

	p1 := publishConceptPage(t, svc, "p1", "c1", "概念一", []string{"pt1"})
	p2 := publishConceptPage(t, svc, "p2", "c2", "概念二", []string{"pt2"})

	shell := &Page{PageID: "topic1", PageType: PageTypeTopic, Title: "壳页", Content: "", Status: StatusDraft}
	if err := svc.store.InsertPage(shell); err != nil {
		t.Fatalf("insert topic shell: %v", err)
	}
	if err := svc.store.UpsertPageRelation("topic1", p1.PageID, RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatalf("insert contains p1: %v", err)
	}
	if err := svc.store.UpsertPageRelation("topic1", p2.PageID, RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatalf("insert contains p2: %v", err)
	}

	members, err := svc.store.ListTopicMemberPages("topic1")
	if err != nil {
		t.Fatalf("list topic member pages: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	titles := map[string]bool{}
	for _, m := range members {
		titles[m.Title] = true
		if m.Content == "" {
			t.Errorf("expected full page content for member %s, got empty", m.PageID)
		}
	}
	if !titles["概念一"] || !titles["概念二"] {
		t.Errorf("unexpected member titles: %+v", titles)
	}
}

func TestListUnassignedConceptPages_ExcludesTopicMembers(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedConcept(t, db, "c2", "d1", "Concept Two")

	standalone := publishConceptPage(t, svc, "p1", "c1", "独立概念", []string{"pt1"})
	member := publishConceptPage(t, svc, "p2", "c2", "已归属概念", []string{"pt2"})

	shell := &Page{PageID: "topic1", PageType: PageTypeTopic, Title: "壳页", Content: "", Status: StatusDraft}
	if err := svc.store.InsertPage(shell); err != nil {
		t.Fatalf("insert topic shell: %v", err)
	}
	if err := svc.store.UpsertPageRelation("topic1", member.PageID, RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatalf("insert contains: %v", err)
	}

	unassigned, err := svc.store.ListUnassignedConceptPages()
	if err != nil {
		t.Fatalf("list unassigned concept pages: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].PageID != standalone.PageID {
		t.Fatalf("expected only the standalone page, got %+v", unassigned)
	}
}
