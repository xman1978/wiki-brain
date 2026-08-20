package study

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
)

// seedDomainSourcePoint inserts a minimal domain → source → knowledge_unit →
// knowledge_point chain so DomainIDsForPoint / buildObservedConditions have
// something to resolve against. withDomain=false leaves sources.domain_id
// NULL, exercising the "no domain found, skip normalization" path.
func seedDomainSourcePoint(t *testing.T, db *sql.DB, domainID, sourceID, unitID, pointID string, withDomain bool) {
	t.Helper()
	if withDomain {
		if _, err := db.Exec(`INSERT INTO domains (domain_id, name) VALUES (?, ?)`, domainID, domainID); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
	}
	var domainCol interface{}
	if withDomain {
		domainCol = domainID
	}
	if _, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id) VALUES (?, 't', 'md', 'f.md', 'f.md', 'f.md', 'done', ?)`,
		sourceID, domainCol); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version) VALUES (?, ?, 'c', 1, 1, 'done', 'v1')`,
		unitID, sourceID); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type) VALUES (?, ?, ?, 'p', 'fact')`,
		pointID, unitID, sourceID); err != nil {
		t.Fatalf("seed point: %v", err)
	}
}

func TestStore_DomainIDsForPoint(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedDomainSourcePoint(t, db, "d1", "s1", "u1", "p1", true)
	seedDomainSourcePoint(t, db, "", "s2", "u2", "p2", false)

	got, err := store.DomainIDsForPoint("p1")
	if err != nil {
		t.Fatalf("domain ids for point: %v", err)
	}
	if len(got) != 1 || got[0] != "d1" {
		t.Fatalf("expected [d1], got %v", got)
	}

	got, err = store.DomainIDsForPoint("p2")
	if err != nil {
		t.Fatalf("domain ids for point (no domain): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty domain ids, got %v", got)
	}
}

// seedTraceQuad inserts a confident trace citing pointID with an explicit
// four-tuple (seedTrace hardcodes intent/audience/constraint empty and
// subject=questionTerms, which doesn't let us vary subject wording).
func seedTraceQuad(t *testing.T, db *sql.DB, traceID, answerID, pointID, subject string) {
	t.Helper()
	pidsJSON := `["` + pointID + `"]`
	_, err := db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids, subject, intent, audience, constraint_text)
		VALUES (?, ?, ?, ?, ?, 'confident', 'short', ?, ?, '', '', '')`,
		traceID, answerID, subject, "hash_"+traceID, subject, pidsJSON, subject)
	if err != nil {
		t.Fatalf("seed trace quad: %v", err)
	}
}

// TestBuildObservedConditions_TupleNormEnabled_MergesParaphrases verifies
// the 2026-08-20 construction-side normalization: two confident traces whose
// subjects are near-duplicate paraphrases (differing by one token, Jaccard
// well above the 0.8 default threshold once intent/audience/constraint are
// all empty and trivially agree) collapse into a single ObservedCondition
// when questionTupleNormEnabled, and stay split when disabled — the
// pre-existing, unchanged behavior.
func TestBuildObservedConditions_TupleNormEnabled_MergesParaphrases(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationStore := activation.NewStore(db)
	activationSvc := activation.NewService(activationStore, activation.NewMatcher(activationStore))
	activationSvc.SetTupleNormalizer(activation.NewTupleNormalizer(activationStore, activation.TupleNormConfig{}))

	seedDomainSourcePoint(t, db, "d1", "s1", "u1", "p1", true)
	seedAnswer(t, db, "a1")
	seedAnswer(t, db, "a2")
	seedTraceQuad(t, db, "t1", "a1", "p1", "如何报销差旅费")
	seedTraceQuad(t, db, "t2", "a2", "p1", "怎么报销差旅费")

	svcEnabled := NewService(store, testConfig(), activationSvc, nil, 0, 0, 0, true)
	conds, err := svcEnabled.buildObservedConditions("p1")
	if err != nil {
		t.Fatalf("buildObservedConditions (enabled): %v", err)
	}
	if len(conds) != 1 {
		t.Fatalf("expected 1 merged condition when tuple norm enabled, got %d: %+v", len(conds), conds)
	}
	if conds[0].SuccessCount != 2 {
		t.Fatalf("expected merged condition SuccessCount=2, got %d", conds[0].SuccessCount)
	}

	svcDisabled := NewService(store, testConfig(), activationSvc, nil, 0, 0, 0, false)
	condsRaw, err := svcDisabled.buildObservedConditions("p1")
	if err != nil {
		t.Fatalf("buildObservedConditions (disabled): %v", err)
	}
	if len(condsRaw) != 2 {
		t.Fatalf("expected 2 separate conditions when tuple norm disabled, got %d: %+v", len(condsRaw), condsRaw)
	}
	for _, c := range condsRaw {
		if c.SuccessCount != 1 {
			t.Fatalf("expected each unmerged condition SuccessCount=1, got %d", c.SuccessCount)
		}
	}
}

// TestBuildObservedConditions_TupleNormEnabled_NoDomainSkipsNormalization
// exercises the "point has no resolvable domain" guard — normalization
// should be skipped entirely (mirrors Retrieval's qc.DomainResolved guard),
// leaving raw-quad grouping behavior unchanged even though the flag is on.
func TestBuildObservedConditions_TupleNormEnabled_NoDomainSkipsNormalization(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	activationStore := activation.NewStore(db)
	activationSvc := activation.NewService(activationStore, activation.NewMatcher(activationStore))
	activationSvc.SetTupleNormalizer(activation.NewTupleNormalizer(activationStore, activation.TupleNormConfig{}))

	seedDomainSourcePoint(t, db, "", "s1", "u1", "p1", false)
	seedAnswer(t, db, "a1")
	seedAnswer(t, db, "a2")
	seedTraceQuad(t, db, "t1", "a1", "p1", "如何报销差旅费")
	seedTraceQuad(t, db, "t2", "a2", "p1", "怎么报销差旅费")

	svc := NewService(store, testConfig(), activationSvc, nil, 0, 0, 0, true)
	conds, err := svc.buildObservedConditions("p1")
	if err != nil {
		t.Fatalf("buildObservedConditions: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("expected 2 separate conditions when no domain resolves, got %d: %+v", len(conds), conds)
	}
}
