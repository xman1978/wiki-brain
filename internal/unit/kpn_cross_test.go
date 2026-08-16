package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

func seedEntry(t *testing.T, db *sql.DB, conceptID, domainID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO entries (entry_id, domain_id, name) VALUES (?, ?, ?)`, conceptID, domainID, name); err != nil {
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

func seedKUWithEntry(t *testing.T, store *Store, unitID, sourceID, conceptID, center string) {
	t.Helper()
	ku := &KnowledgeUnit{UnitID: unitID, SourceID: sourceID, Center: center, LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if conceptID != "" {
		ku.EntryID = sql.NullString{String: conceptID, Valid: true}
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
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")

	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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

// fakeEntryNotifier stands in for entry.Service in unit-package tests
// (docs/impl/v1/kpn.md 步骤 3: orphan KPs are handed to the concept
// evolution module instead of falling back to a same-domain candidate pool).
type fakeEntryNotifier struct {
	calls           []fakeEntryProposeCall
	err             error
	conceptRefs     []string
	pendingPointIDs []string
}

type fakeEntryProposeCall struct {
	domainID, suggestedName, suggestedDescription, suggestedBoundary, kind, entity, sourceID string
	pointIDs                                                                                 []string
}

func (f *fakeEntryNotifier) ProposeAddCandidate(domainID, suggestedName, suggestedDescription, suggestedBoundary, kind, entity string, pointIDs []string, sourceID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, fakeEntryProposeCall{domainID, suggestedName, suggestedDescription, suggestedBoundary, kind, entity, sourceID, pointIDs})
	return "candidate-1", nil
}

func (f *fakeEntryNotifier) ListActiveEntryReferences(domainID string) ([]string, error) {
	return nil, nil
}

func (f *fakeEntryNotifier) ListActiveConceptEntryReferences(domainID string) ([]string, error) {
	return f.conceptRefs, nil
}

func (f *fakeEntryNotifier) ListPendingAddPointIDs(domainID string) ([]string, error) {
	return f.pendingPointIDs, nil
}

func seedEntryWithKind(t *testing.T, db *sql.DB, entryID, domainID, name, kind string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO entries (entry_id, domain_id, name, kind) VALUES (?, ?, ?, ?)`, entryID, domainID, name, kind); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
}

// TestCrossSourceKPN_UnmatchedFactCluster_DoesNotAutoCreateCandidate covers
// the classify-then-branch orphan flow: a KU classified as fact that cannot
// match any existing fact entry AND cannot attach to any domain concept
// dimension must not auto-create a candidate (left orphan for a human).
func TestCrossSourceKPN_UnmatchedFactCluster_DoesNotAutoCreateCandidate(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeEntryNotifier{}
	svc.SetEntryNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	// Neither KU has a entry_id — must NOT fall back to domain grouping;
	// both points route to the concept evolution module instead.
	seedKUWithEntry(t, store, "ku-new", "new-src", "", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [{"unit_id": "ku-new", "kind": "fact"}]}`,
	})
	// Freeform must not run for fact leftovers — if it does, this would create
	// a candidate and fail the assertion below.
	fake.SetResponse("kpn_entry_propose.md", llm.FakeResponse{
		Output: `{"clusters": [{"suggested_name": "新主题", "suggested_description": "描述", "suggested_boundary": "边界", "point_ids": ["kp-new"]}]}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 0 || result.Batches != 0 {
		t.Fatalf("expected no cross-Source relations without entry_id, got %+v", result)
	}
	if result.EntryCandidatesTouched != 0 {
		t.Fatalf("expected 0 concept candidates touched (unmatched fact must not auto-create), got %d", result.EntryCandidatesTouched)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no ProposeAddCandidate call, got %d: %+v", len(notifier.calls), notifier.calls)
	}
}

func TestProposeEntriesForOrphans_ExcludesPendingCandidatePoints(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeEntryNotifier{pendingPointIDs: []string{"kp-pending"}}
	svc.SetEntryNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "src-a", "d1")
	seedKUWithEntry(t, store, "ku-pending", "src-a", "", "pending topic")
	seedKP(t, store, "kp-pending", "ku-pending", "src-a", "already on a pending candidate")
	seedKUWithEntry(t, store, "ku-free", "src-a", "", "free topic")
	seedKP(t, store, "kp-free", "ku-free", "src-a", "still orphan")

	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [{"unit_id": "ku-free", "kind": "concept"}]}`,
	})
	fake.SetResponse("kpn_entry_propose.md", llm.FakeResponse{
		Output: `{"clusters": [{"suggested_name": "自由主题", "suggested_description": "desc", "suggested_boundary": "边界", "point_ids": ["kp-free"]}]}`,
	})

	touched, err := svc.ProposeEntriesForDomainOrphans(context.Background(), "d1")
	if err != nil {
		t.Fatalf("ProposeEntriesForDomainOrphans: %v", err)
	}
	if touched != 1 {
		t.Fatalf("expected 1 proposal, got %d", touched)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 ProposeAddCandidate call, got %d: %+v", len(notifier.calls), notifier.calls)
	}
	if got := notifier.calls[0].pointIDs; len(got) != 1 || got[0] != "kp-free" {
		t.Errorf("point_ids = %v, want [kp-free] (pending point excluded)", got)
	}
}

func TestProposeEntriesForOrphans_SameKindMatchWritesEntryID(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeEntryNotifier{}
	svc.SetEntryNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedEntryWithKind(t, db, "fact-1", "d1", "Oracle RAC", "fact")
	seedSourceWithDomain(t, db, "src-a", "d1")
	seedKUWithEntry(t, store, "ku-a", "src-a", "", "Oracle RAC 安装")
	seedKP(t, store, "kp-a", "ku-a", "src-a", "RAC 安装步骤")

	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [{"unit_id": "ku-a", "kind": "fact"}]}`,
	})
	fake.SetResponse("unit_entry_match.md", llm.FakeResponse{
		Output: `{"matches": [{"unit_id": "ku-a", "entry_id": "fact-1"}]}`,
	})

	touched, err := svc.ProposeEntriesForDomainOrphans(context.Background(), "d1")
	if err != nil {
		t.Fatalf("ProposeEntriesForDomainOrphans: %v", err)
	}
	if touched != 0 {
		t.Fatalf("expected 0 new candidates when matching existing entry, got %d", touched)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("expected no ProposeAddCandidate, got %+v", notifier.calls)
	}
	units, err := store.GetUnitsByIDs([]string{"ku-a"})
	if err != nil || len(units) != 1 {
		t.Fatalf("GetUnitsByIDs: %v %+v", err, units)
	}
	if !units[0].EntryID.Valid || units[0].EntryID.String != "fact-1" {
		t.Errorf("entry_id = %+v, want fact-1", units[0].EntryID)
	}
}

func TestProposeEntriesForOrphans_FactPathCreatesEntityConceptCandidate(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeEntryNotifier{
		conceptRefs: []string{"c-backup\t备份\t备份相关\t关注备份操作"},
	}
	svc.SetEntryNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedEntryWithKind(t, db, "c-backup", "d1", "备份", "concept")
	seedSourceWithDomain(t, db, "src-a", "d1")
	seedKUWithEntry(t, store, "ku-a", "src-a", "", "MySQL 备份策略")
	seedKP(t, store, "kp-a", "ku-a", "src-a", "全量备份每周一次")

	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [{"unit_id": "ku-a", "kind": "fact"}]}`,
	})
	// No existing fact entry match — falls through to concept-dimension path.
	fake.SetResponse("unit_entry_match.md", llm.FakeResponse{
		Output: `{"matches": []}`,
	})
	fake.SetResponse("kpn_orphan_fact_match.md", llm.FakeResponse{
		Output: `{"matches": [{"point_index": 0, "matched_concept_id": "c-backup"}]}`,
	})
	fake.SetResponse("kpn_fact_group_entity.md", llm.FakeResponse{
		Output: `{"entity": "MySQL", "entity_type": "数据库", "description": "MySQL 备份", "boundary": "仅 MySQL"}`,
	})

	touched, err := svc.ProposeEntriesForDomainOrphans(context.Background(), "d1")
	if err != nil {
		t.Fatalf("ProposeEntriesForDomainOrphans: %v", err)
	}
	if touched != 1 {
		t.Fatalf("expected 1 fact candidate, got %d", touched)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 ProposeAddCandidate, got %+v", notifier.calls)
	}
	c := notifier.calls[0]
	if c.kind != "fact" {
		t.Errorf("kind = %q, want fact", c.kind)
	}
	if c.suggestedName != "MySQL备份" && c.suggestedName != "MySQL 备份" {
		t.Errorf("suggested_name = %q, want entity+concept join", c.suggestedName)
	}
	if c.entity != "MySQL" {
		t.Errorf("entity = %q, want MySQL", c.entity)
	}
}

func TestProposeEntriesForDomainOrphans_MultiSourceClusterSplitsBySource(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)
	notifier := &fakeEntryNotifier{}
	svc.SetEntryNotifier(notifier)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "src-a", "d1")
	seedSourceWithDomain(t, db, "src-b", "d1")

	// Orphans from two different Sources in the same domain — unlike the
	// per-Source KPN pipeline pass, the domain-wide trigger must see both.
	seedKUWithEntry(t, store, "ku-a", "src-a", "", "topic a")
	seedKP(t, store, "kp-a", "ku-a", "src-a", "content a")
	seedKUWithEntry(t, store, "ku-b", "src-b", "", "topic b")
	seedKP(t, store, "kp-b", "ku-b", "src-b", "content b")

	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [
			{"unit_id": "ku-a", "kind": "concept"},
			{"unit_id": "ku-b", "kind": "concept"}
		]}`,
	})
	fake.SetResponse("kpn_entry_propose.md", llm.FakeResponse{
		Output: `{"clusters": [{"suggested_name": "共同主题", "suggested_description": "desc", "suggested_boundary": "边界", "point_ids": ["kp-a", "kp-b"]}]}`,
	})

	touched, err := svc.ProposeEntriesForDomainOrphans(context.Background(), "d1")
	if err != nil {
		t.Fatalf("ProposeEntriesForDomainOrphans: %v", err)
	}
	if touched != 1 {
		t.Fatalf("expected 1 cluster touched, got %d", touched)
	}
	// One LLM-proposed cluster spanning two Sources must fan out into one
	// ProposeAddCandidate call per source so evidence.source_ids ends up
	// complete via the existing per-source merge path.
	if len(notifier.calls) != 2 {
		t.Fatalf("expected 2 ProposeAddCandidate calls (one per source), got %d: %+v", len(notifier.calls), notifier.calls)
	}
	bySource := map[string][]string{}
	for _, c := range notifier.calls {
		bySource[c.sourceID] = c.pointIDs
	}
	if got := bySource["src-a"]; len(got) != 1 || got[0] != "kp-a" {
		t.Errorf("src-a call point_ids = %v, want [kp-a]", got)
	}
	if got := bySource["src-b"]; len(got) != 1 || got[0] != "kp-b" {
		t.Errorf("src-b call point_ids = %v, want [kp-b]", got)
	}
}

func TestCrossSourceKPN_NoOrphanProposal_WhenNoEntryNotifierSet(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")

	result, err := svc.CrossSourceKPN(context.Background(), "new-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.EntryCandidatesTouched != 0 {
		t.Errorf("expected 0 touched without a notifier, got %d", result.EntryCandidatesTouched)
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
	seedKUWithEntry(t, store, "ku-new", "new-src", "", "new topic")
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
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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

	if result2.Batches != 0 {
		t.Errorf("expected 0 batches on re-trigger (pair already seen, nothing left to ask), got %d", result2.Batches)
	}
}

// TestCrossSourceKPN_SeenPairsSkipRedundantLLMCall is the regression test for
// the 2026-08-16 idempotency fix (根因二): re-triggering kpn-cross for a
// Source whose candidates haven't changed must not re-send the same pair to
// the LLM a second time — kpn_cross_pairs_seen should make the second run a
// no-op batch-wise, not just relation-count-wise (a batch that re-asks and
// happens to get the same answer would still look "idempotent" by relation
// count alone while burning an LLM call every trigger, which is the actual
// production symptom this fix addresses).
func TestCrossSourceKPN_SeenPairsSkipRedundantLLMCall(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": []}`,
	})

	if _, err := svc.CrossSourceKPN(context.Background(), "new-src"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	callsAfterFirst := len(fake.Calls())
	if callsAfterFirst == 0 {
		t.Fatalf("expected first run to call the LLM at least once")
	}

	if _, err := svc.CrossSourceKPN(context.Background(), "new-src"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	callsAfterSecond := len(fake.Calls())
	if callsAfterSecond != callsAfterFirst {
		t.Errorf("expected no additional LLM calls on re-trigger of an unchanged pair, first=%d second=%d",
			callsAfterFirst, callsAfterSecond)
	}
}

// TestMatchEntries_DropsStaleCrossRelationsOnEntryReclassify covers the fix
// for 2026-08-08's stale-relation finding: a Source's scope=cross KPN
// relations are built against whatever entry_id CrossSourceKPN grouped its
// points under at the time; if a later manual domain reassignment
// (source.Service.SetDomain -> MatchEntries) reclassifies those points into
// a different (or no) entry_id, the old cross relations no longer
// correspond to any current entry_id grouping and must be dropped rather
// than left as unexplained edges between points that no longer share an
// entry.
func TestMatchEntries_DropsStaleCrossRelationsOnEntryReclassify(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
	seedKP(t, store, "kp-existing", "ku-existing", "existing-src", "existing content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [{"from": "kp-new", "to": "kp-existing", "type": "related"}]}`,
	})
	if _, err := svc.CrossSourceKPN(context.Background(), "new-src"); err != nil {
		t.Fatalf("initial cross kpn: %v", err)
	}
	rels, _ := store.GetRelationsByPointID("kp-new", "")
	if len(rels) != 1 {
		t.Fatalf("expected 1 relation seeded before reclassify, got %d", len(rels))
	}

	// Reassign new-src to a domain with no entries at all — matchEntries
	// clears entry_id and finds nothing to reassign it to, so kp-new's KU
	// ends up with no entry_id, no longer matching kp-existing's c1 grouping.
	seedDomain(t, db, "d2", "D2")
	svc.MatchEntries(context.Background(), "new-src", "d2")

	rels, _ = store.GetRelationsByPointID("kp-new", "")
	if len(rels) != 0 {
		t.Errorf("expected stale cross relation dropped after entry reclassify, got %+v", rels)
	}
}

// TestMatchEntries_ClearsSeenPairsOnReclassify is the regression test for
// the other half of the 2026-08-16 idempotency fix: DeleteCrossPairsSeenBySourceID
// must run alongside DeleteCrossRelationsBySourceID whenever MatchEntries
// regroups a Source's points under a different entry_id — otherwise
// FilterUnseenOpposite would see the old (entry_id=c1) seen-pairs rows and
// wrongly treat the point as "already asked" under the new (entry_id=c2)
// grouping, permanently blocking it from ever being cross-matched again.
func TestMatchEntries_ClearsSeenPairsOnReclassify(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedEntry(t, db, "c1", "d1", "C1")
	seedEntry(t, db, "c2", "d1", "C2")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing-c1", "existing-src", "c1", "existing c1 topic")
	seedKP(t, store, "kp-existing-c1", "ku-existing-c1", "existing-src", "existing c1 content")
	seedKUWithEntry(t, store, "ku-existing-c2", "existing-src", "c2", "existing c2 topic")
	seedKP(t, store, "kp-existing-c2", "ku-existing-c2", "existing-src", "existing c2 content")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": []}`,
	})

	// Initial match under c1: kp-new gets compared against kp-existing-c1
	// and the pair is recorded as seen, with no relation (LLM said none).
	if _, err := svc.CrossSourceKPN(context.Background(), "new-src"); err != nil {
		t.Fatalf("initial cross kpn: %v", err)
	}
	seenBefore, err := store.FilterUnseenOpposite([]string{"kp-new"}, []KnowledgePoint{{PointID: "kp-existing-c1"}})
	if err != nil {
		t.Fatalf("FilterUnseenOpposite: %v", err)
	}
	if len(seenBefore) != 0 {
		t.Fatalf("expected kp-new/kp-existing-c1 pair to be recorded as seen after initial run, got %+v", seenBefore)
	}

	// MatchEntries clears ku-new's entry_id and re-derives it via LLM
	// classify+match — configure the fakes so it lands on c2 this time
	// (simulating a manual domain reassignment surfacing a better match).
	fake.SetResponse("unit_kind_classify.md", llm.FakeResponse{
		Output: `{"classifications": [{"unit_id": "ku-new", "kind": "concept"}]}`,
	})
	fake.SetResponse("unit_entry_match.md", llm.FakeResponse{
		Output: `{"matches": [{"unit_id": "ku-new", "entry_id": "c2"}]}`,
	})
	svc.MatchEntries(context.Background(), "new-src", "d1")

	// Under the new c2 grouping, kp-new must be free to be asked about
	// kp-existing-c2 — if the seen-pairs ledger wasn't cleared, a stale row
	// keyed on the old c1 pairing wouldn't block this (different opposite
	// point_id), but a bug that cleared relations without clearing seen-pairs
	// would still leave kp-new/kp-existing-c1 marked seen forever, which is
	// harmless post-reclassify but proves the cleanup ran; the real
	// assertion is that the new pairing actually got asked at all.
	rels, _ := store.GetRelationsByPointID("kp-new", "")
	for _, r := range rels {
		if r.Scope == RelationScopeCross {
			t.Fatalf("expected no stale cross relation surviving reclassify, got %+v", r)
		}
	}
	seenAfter, err := store.FilterUnseenOpposite([]string{"kp-new"}, []KnowledgePoint{{PointID: "kp-existing-c2"}})
	if err != nil {
		t.Fatalf("FilterUnseenOpposite after reclassify: %v", err)
	}
	if len(seenAfter) != 0 {
		t.Errorf("expected kp-new/kp-existing-c2 pair to have been asked (and recorded seen) by MatchEntries's rebuild, got unseen=%+v", seenAfter)
	}
}

func TestCrossSourceKPN_RejectsWrongDirection(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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
	seedEntry(t, db, "c1", "d1", "C1")
	seedEntry(t, db, "c2", "d1", "C2")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")

	// Two separate concept groups, each with a matching opposite — would be
	// 2 batches without the cap.
	seedKUWithEntry(t, store, "ku-new-1", "new-src", "c1", "topic1")
	seedKP(t, store, "kp-new-1", "ku-new-1", "new-src", "content1")
	seedKUWithEntry(t, store, "ku-existing-1", "existing-src", "c1", "topic1")
	seedKP(t, store, "kp-existing-1", "ku-existing-1", "existing-src", "content1")

	seedKUWithEntry(t, store, "ku-new-2", "new-src", "c2", "topic2")
	seedKP(t, store, "kp-new-2", "ku-new-2", "new-src", "content2")
	seedKUWithEntry(t, store, "ku-existing-2", "existing-src", "c2", "topic2")
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
	if totalOpp != 2 {
		t.Errorf("expected both opposite points placed exactly once, got total %d from 2 available", totalOpp)
	}
}

// TestSplitCrossBatch_NeverWastesAChunkWhileOppositeRemains is the
// regression test for the 2026-08-16 fix: a newPoints chunk landing on
// exactly maxSize used to get 0 budget for opposite even while opposite
// candidates were still waiting to be placed — that chunk compared nothing
// (crossKPNBatch no-ops on empty opposite) yet still counted as a "batch"
// every trigger, so CrossSourceKPN's batches count could never reach 0 for
// a group whose own new-point count exceeded crossBatchMaxSize, even after
// every real pair had already been covered by kpn_cross_pairs_seen.
func TestSplitCrossBatch_NeverWastesAChunkWhileOppositeRemains(t *testing.T) {
	newPoints := make([]KnowledgePoint, 80)
	for i := range newPoints {
		newPoints[i] = KnowledgePoint{PointID: fmt.Sprintf("n%d", i)}
	}
	opposite := []KnowledgePoint{{PointID: "o1"}}

	chunks := splitCrossBatch(newPoints, opposite, 60)

	for _, c := range chunks {
		if len(c.newPoints) == 60 && len(c.oppositePoints) == 0 {
			t.Fatalf("got a maxSize-sized newPoints chunk with 0 opposite budget while opposite candidates existed: %+v", chunks)
		}
	}

	totalNew, totalOpp := 0, 0
	for _, c := range chunks {
		totalNew += len(c.newPoints)
		totalOpp += len(c.oppositePoints)
	}
	if totalNew != 80 {
		t.Errorf("expected all 80 new points distributed, got %d", totalNew)
	}
	if totalOpp != 1 {
		t.Errorf("expected the 1 opposite point placed exactly once, got %d", totalOpp)
	}
}

func TestGroupPointsForCrossMatch_SeparatesOrphans(t *testing.T) {
	points := []KnowledgePoint{
		{PointID: "p1", UnitID: "u1"},
		{PointID: "p2", UnitID: "u2"},
		{PointID: "p3", UnitID: "u3"},
	}
	unitEntryMap := map[string]string{"u1": "c1"} // u2, u3 have no concept
	groups, orphans := groupPointsForCrossMatch(points, unitEntryMap)

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
	seedEntry(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "new-src", "d1")
	seedSourceWithDomain(t, db, "existing-src", "d1")
	seedKUWithEntry(t, store, "ku-new", "new-src", "c1", "new topic")
	seedKP(t, store, "kp-new", "ku-new", "new-src", "new content")
	seedKUWithEntry(t, store, "ku-existing", "existing-src", "c1", "existing topic")
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
