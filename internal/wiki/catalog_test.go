package wiki

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation"
)

func TestListCatalog_GroupsByDomain(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "Alpha领域")
	seedDomain(t, db, "d2", "Beta领域")
	if _, err := db.Exec(`UPDATE domains SET description = ? WHERE domain_id = ?`, "甲的说明", "d1"); err != nil {
		t.Fatal(err)
	}
	seedConcept(t, db, "c1", "d1", "概念甲")
	seedConcept(t, db, "c2", "d2", "概念乙")
	seedConcept(t, db, "c3", "d1", "概念丙")

	now := time.Now().UTC()
	mustInsertPage(t, store, &Page{
		PageID: "p-pub", PageType: PageTypeConcept, ConceptID: nullStr("c1"),
		Title: "已发布页", Content: "body", Status: StatusPublished, Summary: "发布摘要",
		CompiledAt: sqlNullTime(now),
	})
	mustInsertPage(t, store, &Page{
		PageID: "p-draft", PageType: PageTypeConcept, ConceptID: nullStr("c2"),
		Title: "草稿页", Content: "body", Status: StatusDraft, Summary: "",
	})
	mustInsertPage(t, store, &Page{
		PageID: "p-arch", PageType: PageTypeConcept, ConceptID: nullStr("c1"),
		Title: "归档页", Content: "body", Status: StatusArchived, Summary: "旧摘要",
	})

	// Topic spanning both domains via members in d1 and d2.
	mustInsertPage(t, store, &Page{
		PageID: "p-topic", PageType: PageTypeTopic, Title: "跨领域主题",
		Content: "topic body", Status: StatusPublished, Summary: "主题摘要",
	})
	if err := store.UpsertPageRelation("p-topic", "p-pub", RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertPageRelation("p-topic", "p-draft", RelationContains, DerivedFromCompile, "{}"); err != nil {
		t.Fatal(err)
	}

	actStore := activation.NewStore(db)
	if err := actStore.InsertLearningResult(&activation.LearningResult{
		Action: activation.ActionWikiCandidate, ObjectType: activation.ObjectTypeWikiPage,
		ObjectID: "c3", Reason: "ready for compile", Status: activation.ResultPendingConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	// Candidate for c1 should be skipped: active published page already exists.
	if err := actStore.InsertLearningResult(&activation.LearningResult{
		Action: activation.ActionWikiCandidate, ObjectType: activation.ObjectTypeWikiPage,
		ObjectID: "c1", Reason: "should skip", Status: activation.ResultPendingConfirm,
	}); err != nil {
		t.Fatal(err)
	}

	catalog, err := store.ListCatalog()
	if err != nil {
		t.Fatalf("ListCatalog: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(catalog))
	}
	byID := map[string]CatalogDomain{}
	for _, d := range catalog {
		byID[d.DomainID] = d
	}
	if catalog[0].DomainID != "d1" || catalog[1].DomainID != "d2" {
		t.Fatalf("domains not ordered by name: %+v", catalog)
	}

	d1 := byID["d1"]
	// d1: candidate c3, published p-pub, archived p-arch, topic p-topic
	if d1.WikiCount != 4 {
		t.Fatalf("d1 wiki_count=%d want 4; pages=%+v", d1.WikiCount, d1.Pages)
	}
	if d1.Pages[0].Status != CatalogStatusPendingCompile || d1.Pages[0].Title != "概念丙" {
		t.Fatalf("d1 first card want pending 概念丙, got %+v", d1.Pages[0])
	}
	if d1.Pages[0].Description == "" {
		t.Fatal("candidate description should fall back to concept desc or reason")
	}

	var sawTopicD1, sawPub, sawArch bool
	for _, c := range d1.Pages {
		switch c.PageID {
		case "p-topic":
			sawTopicD1 = true
			if c.Description != "主题摘要" {
				t.Errorf("topic description=%q", c.Description)
			}
		case "p-pub":
			sawPub = true
			if c.Description != "发布摘要" {
				t.Errorf("published description=%q", c.Description)
			}
		case "p-arch":
			sawArch = true
		}
	}
	if !sawTopicD1 || !sawPub || !sawArch {
		t.Fatalf("d1 missing cards: topic=%v pub=%v arch=%v", sawTopicD1, sawPub, sawArch)
	}

	d2 := byID["d2"]
	// d2: draft p-draft, topic p-topic
	if d2.WikiCount != 2 {
		t.Fatalf("d2 wiki_count=%d want 2; pages=%+v", d2.WikiCount, d2.Pages)
	}
	if d2.Pages[0].Status != StatusDraft || d2.Pages[0].PageID != "p-draft" {
		t.Fatalf("d2 first card want draft p-draft, got %+v", d2.Pages[0])
	}
	// Empty summary → concept description.
	if d2.Pages[0].Description != "概念乙 的描述" {
		t.Errorf("draft description fallback=%q", d2.Pages[0].Description)
	}
	if d2.Pages[1].PageID != "p-topic" {
		t.Fatalf("d2 second card want topic, got %+v", d2.Pages[1])
	}

	// HTTP shape
	svc := &Service{store: store}
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/wiki/catalog", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /wiki/catalog status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp []CatalogDomain
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 || resp[0].WikiCount != 4 {
		t.Fatalf("http catalog unexpected: %+v", resp)
	}
}

func mustInsertPage(t *testing.T, store *Store, p *Page) {
	t.Helper()
	if p.SourcePointIDs == "" {
		p.SourcePointIDs = "[]"
	}
	if p.SourceUnitIDs == "" {
		p.SourceUnitIDs = "[]"
	}
	if p.SourceLinkIDs == "" {
		p.SourceLinkIDs = "[]"
	}
	if p.CompiledFrom == "" {
		p.CompiledFrom = "[]"
	}
	if err := store.InsertPage(p); err != nil {
		t.Fatalf("insert page %s: %v", p.PageID, err)
	}
	if p.Status != StatusDraft {
		if err := store.UpdatePageStatus(p.PageID, p.Status); err != nil {
			t.Fatalf("set status %s: %v", p.PageID, err)
		}
	}
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}

func sqlNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: true}
}
