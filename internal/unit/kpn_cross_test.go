package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func seedDomain(t *testing.T, db *sql.DB, domainID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO domains (domain_id, name) VALUES (?, ?)`, domainID, name); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
}

func seedConcept(t *testing.T, db *sql.DB, conceptID, domainID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO concepts (concept_id, domain_id, name) VALUES (?, ?, ?)`, conceptID, domainID, name); err != nil {
		t.Fatalf("seed concept: %v", err)
	}
}

func seedSourceWithDomain(t *testing.T, db *sql.DB, sourceID, domainID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES (?, ?, 'markdown', ?, '/tmp/x.md', '/tmp/x.md', 'completed', ?)`,
		sourceID, sourceID, sourceID+".md", nullableString(domainID))
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func seedKUWithConcept(t *testing.T, store *Store, unitID, sourceID, conceptID, center string) {
	t.Helper()
	ku := &KnowledgeUnit{UnitID: unitID, SourceID: sourceID, Center: center, LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if conceptID != "" {
		ku.ConceptID = sql.NullString{String: conceptID, Valid: true}
	}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("seed ku: %v", err)
	}
}

func seedKP(t *testing.T, store *Store, pointID, unitID, sourceID, content string) {
	t.Helper()
	kp := &KnowledgePoint{PointID: pointID, UnitID: unitID, SourceID: sourceID, Content: content, PointType: "fact"}
	if err := store.InsertPoint(kp); err != nil {
		t.Fatalf("seed kp: %v", err)
	}
}

func TestCrossSourceKPN_MatchByConcept(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")

	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-new", "to": "kp-existing", "type": "related"}]}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 1 {
		t.Fatalf("expected 1 relation created, got %d", result.RelationsCreated)
	}
	if result.Batches != 1 {
		t.Errorf("expected 1 batch, got %d", result.Batches)
	}

	rels, err := store.GetRelationsByPointID("kp-new", "")
	if err != nil {
		t.Fatalf("get relations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(rels))
	}
	rel := rels[0]
	if rel.Scope != RelationScopeCross {
		t.Errorf("scope = %q, want cross", rel.Scope)
	}
	if rel.SourcePointID != "kp-new" {
		t.Errorf("expected from=kp-new (new KP), got %q", rel.SourcePointID)
	}
	if rel.Direction != "bidirectional" {
		t.Errorf("direction = %q, want bidirectional", rel.Direction)
	}
}

// fakeConceptNotifier stands in for concept.Service in unit-package tests
// (docs/impl/v1/kpn.md 步骤 3: orphan KPs are handed to the concept
// evolution module instead of falling back to a same-domain candidate pool).
type fakeConceptNotifier struct {
	calls []fakeConceptProposeCall
	err   error
}

type fakeConceptProposeCall struct {
	domainID, suggestedName, suggestedDescription, sourceID string
	pointIDs                                                []string
}

func (f *fakeConceptNotifier) ProposeAddCandidate(domainID, suggestedName, suggestedDescription string, pointIDs []string, sourceID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, fakeConceptProposeCall{domainID, suggestedName, suggestedDescription, sourceID, pointIDs})
	return "candidate-1", nil
}

func TestCrossSourceKPN_OrphanPointsRouteToConceptCandidate_WhenNoConcept(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeConceptNotifier{}
	svc.SetConceptNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	// Neither KU has a concept_id — must NOT fall back to domain grouping;
	// both points route to the concept evolution module instead.
	seedKUWithConcept(t, store, "ku-new", "new-src", "", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_concept_propose.md", llm.FakeResponse{
		Output: `{"suggested_name": "新主题", "suggested_description": "描述"}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 0 || result.Batches != 0 {
		t.Fatalf("expected no cross-Source relations without concept_id, got %+v", result)
	}
	if result.ConceptCandidatesTouched != 1 {
		t.Fatalf("expected 1 concept candidate touched, got %d", result.ConceptCandidatesTouched)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 ProposeAddCandidate call, got %d", len(notifier.calls))
	}
	call := notifier.calls[0]
	if call.domainID != "d1" {
		t.Errorf("domain_id = %q, want d1", call.domainID)
	}
	if call.sourceID != "new-src" {
		t.Errorf("source_id = %q, want new-src", call.sourceID)
	}
	if len(call.pointIDs) != 1 || call.pointIDs[0] != "kp-new" {
		t.Errorf("expected only new-src's own orphan point (kp-new), got %v", call.pointIDs)
	}
}

func TestCrossSourceKPN_NoOrphanProposal_WhenNoConceptNotifierSet(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.ConceptCandidatesTouched != 0 {
		t.Errorf("expected 0 touched without a notifier, got %d", result.ConceptCandidatesTouched)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls, got %d", len(fake.Calls()))
	}
}

func TestCrossSourceKPN_SkipsWhenNoConceptOrDomain(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	// No domain_id on the source at all.
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('new-src', 'new-src', 'markdown', 'x.md', '/tmp/x.md', '/tmp/x.md', 'completed')`)
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedKUWithConcept(t, store, "ku-new", "new-src", "", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 0 || result.Batches != 0 {
		t.Errorf("expected no batches/relations without concept or domain, got %+v", result)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls, got %d", len(fake.Calls()))
	}
}

func TestCrossSourceKPN_Idempotent(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-new", "to": "kp-existing", "type": "related"}]}`,
	})

	if _, err := svc.CrossSourceKPN(context.Background(), "new-src"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	result2, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result2.RelationsCreated != 0 {
		t.Errorf("expected 0 new relations on re-trigger (already exists), got %d", result2.RelationsCreated)
	}

	rels, _ := store.GetRelationsByPointID("kp-new", "")
	if len(rels) != 1 {
		t.Fatalf("expected exactly 1 relation total (no duplicate), got %d", len(rels))
	}
}

func TestCrossSourceKPN_RejectsWrongDirection(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	// Reversed: from=existing (B group), to=new (A group) — must be rejected.
	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-existing", "to": "kp-new", "type": "related"}]}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 0 {
		t.Errorf("expected reversed from/to to be rejected, got %d relations", result.RelationsCreated)
	}
}

func TestCrossSourceKPN_ExcludesNonCurrentOppositeKP(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")
	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'kp-existing'`)

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.Batches != 0 {
		t.Errorf("expected no batches once the only opposite KP is non-current, got %+v", result)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no LLM calls, got %d", len(fake.Calls()))
	}
}

func TestCrossSourceKPN_ContradictsRelation(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-new", "to": "kp-existing", "type": "contradicts"}]}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 1 {
		t.Fatalf("expected 1 contradicts relation, got %d", result.RelationsCreated)
	}

	rels, _ := store.GetRelationsByPointID("kp-new", "cross")
	if len(rels) != 1 || rels[0].RelationType != "contradicts" {
		t.Fatalf("expected 1 cross-scope contradicts relation, got %+v", rels)
	}
}

func TestCrossSourceKPN_BatchCapEnforced(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	svc.cfg.KPN.CrossMaxBatches = 1

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C1")
	seedConcept(t, db, "c2", "d1", "C2")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	// Two separate concept groups, each with a matching opposite — would be
	// 2 batches without the cap.
	seedKUWithConcept(t, store, "ku-new-1", "new-src", "c1", "topic1")
	seedKP(t, store, "kp-new-1", "ku-new-1", "new-src", "content1")
	seedKUWithConcept(t, store, "ku-existing-1", "existing-src", "c1", "topic1")
	seedKP(t, store, "kp-existing-1", "ku-existing-1", "existing-src", "content1")

	seedKUWithConcept(t, store, "ku-new-2", "new-src", "c2", "topic2")
	seedKP(t, store, "kp-new-2", "ku-new-2", "new-src", "content2")
	seedKUWithConcept(t, store, "ku-existing-2", "existing-src", "c2", "topic2")
	seedKP(t, store, "kp-existing-2", "ku-existing-2", "existing-src", "content2")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{Output: `{"relations": []}`})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.Batches != 1 {
		t.Errorf("expected batches capped at 1, got %d", result.Batches)
	}
}

func TestSplitCrossBatch_FitsWithinLimit(t *testing.T) {
	newPoints := []KnowledgePoint{{PointID: "n1"}, {PointID: "n2"}}
	opposite := []KnowledgePoint{{PointID: "o1"}}
	chunks := splitCrossBatch(newPoints, opposite, 60)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0].newPoints) != 2 || len(chunks[0].oppositePoints) != 1 {
		t.Errorf("unexpected chunk contents: %+v", chunks[0])
	}
}

func TestSplitCrossBatch_HardSplitsOversizedNewPoints(t *testing.T) {
	newPoints := make([]KnowledgePoint, 5)
	for i := range newPoints {
		newPoints[i] = KnowledgePoint{PointID: string(rune('a' + i))}
	}
	opposite := []KnowledgePoint{{PointID: "o1"}, {PointID: "o2"}}

	chunks := splitCrossBatch(newPoints, opposite, 2)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (5 new points / maxSize=2), got %d", len(chunks))
	}
	totalNew := 0
	totalOpp := 0
	for _, c := range chunks {
		totalNew += len(c.newPoints)
		totalOpp += len(c.oppositePoints)
		if len(c.newPoints)+len(c.oppositePoints) > 2 {
			t.Errorf("chunk exceeds maxSize: %+v", c)
		}
	}
	if totalNew != 5 {
		t.Errorf("expected all 5 new points distributed, got %d", totalNew)
	}
	if totalOpp > 2 {
		t.Errorf("opposite points should never be reused across chunks, got total %d from only 2 available", totalOpp)
	}
}

func TestGroupPointsForCrossMatch_SeparatesOrphans(t *testing.T) {
	points := []KnowledgePoint{
		{PointID: "p1", UnitID: "u1"},
		{PointID: "p2", UnitID: "u2"},
		{PointID: "p3", UnitID: "u3"},
	}
	unitConceptMap := map[string]string{"u1": "c1"} // u2, u3 have no concept
	groups, orphans := groupPointsForCrossMatch(points, unitConceptMap)

	if len(groups) != 1 || groups[0].id != "c1" || len(groups[0].points) != 1 {
		t.Fatalf("expected 1 concept group (c1, 1 point), got %+v", groups)
	}
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphan points (no concept, no domain fallback), got %d: %+v", len(orphans), orphans)
	}
}

func TestHandler_TriggerCrossKPN(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	handler := NewHandler(svc)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithConcept(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithConcept(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-new", "to": "kp-existing", "type": "related"}]}`,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	req := httptest.NewRequest("POST", "/sources/new-src/kpn-cross", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result CrossKPNResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.RelationsCreated != 1 {
		t.Errorf("expected 1 relation created, got %d", result.RelationsCreated)
	}
}
