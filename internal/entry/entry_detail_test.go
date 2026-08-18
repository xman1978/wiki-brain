package entry

import (
	"database/sql"
	"testing"
)

func TestStore_GetEntryDetail(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")

	d, err := store.GetEntryDetail("c1")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d == nil {
		t.Fatal("expected non-nil detail")
	}
	if d.EntryID != "c1" || d.DomainID != "d1" {
		t.Fatalf("unexpected detail: %+v", d)
	}
	if len(d.Points) != 1 || d.Points[0].PointID != "p1" {
		t.Fatalf("unexpected points: %+v", d.Points)
	}
}

func TestStore_GetEntryDetail_MergedAwayReturnsNil(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "target", "d1", sql.NullString{})
	seedEntry(t, db, "merged", "d1", sql.NullString{String: "target", Valid: true})

	d, err := store.GetEntryDetail("merged")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d != nil {
		t.Fatalf("expected nil for merged-away concept, got %+v", d)
	}
}

func TestStore_UpdateEntryMeta(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})

	if err := store.UpdateEntryMeta("c1", "新名称", "新描述", EntryKindConcept); err != nil {
		t.Fatalf("update concept meta: %v", err)
	}
	d, err := store.GetEntryDetail("c1")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Name != "新名称" || d.Description != "新描述" {
		t.Fatalf("update did not apply: %+v", d)
	}
}

func TestStore_UpdateEntryMeta_MergedAwayFails(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "target", "d1", sql.NullString{})
	seedEntry(t, db, "merged", "d1", sql.NullString{String: "target", Valid: true})

	if err := store.UpdateEntryMeta("merged", "x", "y", EntryKindConcept); err == nil {
		t.Fatal("expected error updating a merged-away concept")
	}
}

func TestStore_AddEntryPoints(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{}) // unclassified
	seedKP(t, db, "p1", "u1", "s1")

	migrated, err := store.AddEntryPoints("c1", []string{"p1"})
	if err != nil {
		t.Fatalf("add concept points: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	d, err := store.GetEntryDetail("c1")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if len(d.Points) != 1 || d.Points[0].PointID != "p1" {
		t.Fatalf("point not attached: %+v", d.Points)
	}
}

func TestStore_AddEntryPoints_SkipsAlreadyClassifiedUnit(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedEntry(t, db, "other", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "other", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")

	migrated, err := store.AddEntryPoints("c1", []string{"p1"})
	if err != nil {
		t.Fatalf("add concept points: %v", err)
	}
	if migrated != 0 {
		t.Fatalf("migrated = %d, want 0 (unit already classified elsewhere)", migrated)
	}
}

func TestStore_RemoveEntryPoint(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")

	unitPointCount, err := store.RemoveEntryPoint("c1", "p1")
	if err != nil {
		t.Fatalf("remove concept point: %v", err)
	}
	if unitPointCount != 1 {
		t.Fatalf("unit_point_count = %d, want 1", unitPointCount)
	}
	d, err := store.GetEntryDetail("c1")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if len(d.Points) != 0 {
		t.Fatalf("expected point removed, got %+v", d.Points)
	}
}

func TestStore_RemoveEntryPoint_ReportsSiblingPoints(t *testing.T) {
	_, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")
	seedKP(t, db, "p2", "u1", "s1")

	unitPointCount, err := store.RemoveEntryPoint("c1", "p1")
	if err != nil {
		t.Fatalf("remove concept point: %v", err)
	}
	if unitPointCount != 2 {
		t.Fatalf("unit_point_count = %d, want 2 (removing unclassifies the whole unit, including p2)", unitPointCount)
	}
}

func TestService_ConfirmAdd_DescriptionFromRequest(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept,
		[]string{"p1"}, nil, ContentDrivenEvidence{Origin: "content_driven", Description: "原始候选描述"}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{SuggestedName: "并发编程", Description: "人工编辑后的描述"}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	d, err := store.GetEntryDetail(result.EntryID)
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Description != "人工编辑后的描述" {
		t.Errorf("description = %q, want the edited value from the confirm request", d.Description)
	}
}

func TestService_CreateManualCandidate_PersistsDescription(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.CreateManualCandidate("d1", "并发编程", "草稿阶段填写的描述", EntryKindConcept, []string{"p1"})
	if err != nil {
		t.Fatal(err)
	}

	// Confirming without overriding description should carry the draft's
	// own description through, same as a content_driven candidate's would.
	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	ev := candidateEvidence(t, c)
	if ev["description"] != "草稿阶段填写的描述" {
		t.Fatalf("evidence.description = %v, want the draft's description to round-trip", ev["description"])
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{Description: "草稿阶段填写的描述"}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	d, err := store.GetEntryDetail(result.EntryID)
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Description != "草稿阶段填写的描述" {
		t.Errorf("description = %q, want the draft's original description", d.Description)
	}
}

func TestStore_ListDomainAddCandidates(t *testing.T) {
	_, store, db := setupService(t)
	seedDomain(t, db, "d1")
	seedDomain(t, db, "d2")

	pending, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "pending one", EntryKindConcept, nil, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "rejected one", EntryKindConcept, nil, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reject(rejected); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertAddCandidate(sql.NullString{String: "d2", Valid: true}, "other domain", EntryKindConcept, nil, nil, AddEvidence{}, "seed", sql.NullString{}); err != nil {
		t.Fatal(err)
	}
	appliedID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "applied one", EntryKindConcept, nil, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmAdd(appliedID, "concept-x", "d1", "applied one", "", "", EntryKindConcept, nil, nil, "seed", sql.NullString{}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListDomainAddCandidates("d1")
	if err != nil {
		t.Fatalf("list domain add candidates: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 candidates (pending + rejected, excluding applied and other-domain), got %d", len(rows))
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.CandidateID] = true
	}
	if !ids[pending] || !ids[rejected] {
		t.Fatalf("missing expected candidates: %+v", ids)
	}
}
