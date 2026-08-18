package entry

import (
	"database/sql"
	"testing"
)

func TestValidateConceptKind(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", EntryKindConcept, false},
		{"concept", EntryKindConcept, false},
		{"fact", EntryKindFact, false},
		{"nonsense", "", true},
	}
	for _, c := range cases {
		got, err := ValidateEntryKind(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ValidateEntryKind(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateEntryKind(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ValidateEntryKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestService_ProposeAddCandidate_PersistsKind covers docs/impl/v1/kpn.md
// 步骤 3's content-driven kind classification: a cluster tagged kind=fact
// by kpn_entry_propose.md must land on the resulting candidate row, and
// survive the confirm-time concept creation onto entries.kind.
func TestService_ProposeAddCandidate_PersistsKind(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := svc.ProposeAddCandidate("d1", "Oracle RAC", "具体数据库产品", "", EntryKindFact, "", []string{"p1"}, "s1", "")
	if err != nil {
		t.Fatal(err)
	}

	c, err := store.GetCandidate(candidateID)
	if err != nil || c == nil {
		t.Fatalf("get candidate: %v", err)
	}
	if c.EntryKind != EntryKindFact {
		t.Fatalf("candidate entry_kind = %q, want %q", c.EntryKind, EntryKindFact)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{DomainID: "d1"}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	d, err := store.GetEntryDetail(result.EntryID)
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Kind != EntryKindFact {
		t.Errorf("concept kind = %q, want %q", d.Kind, EntryKindFact)
	}
}

// TestService_ProposeAddCandidate_InvalidKindRejected covers the validation
// boundary: an unrecognized kind value from a malformed LLM cluster output
// must be rejected, not silently coerced.
func TestService_ProposeAddCandidate_InvalidKindRejected(t *testing.T) {
	svc, _, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	if _, err := svc.ProposeAddCandidate("d1", "topic", "desc", "", "bogus", "", []string{"p1"}, "s1", ""); err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

// TestService_ConfirmAdd_KindOverride covers the confirm dialog's kind
// override path (mirrors SuggestedName/Description overrides) and its
// validation.
func TestService_ConfirmAdd_KindOverride(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept,
		[]string{"p1"}, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryKind: EntryKindFact}, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	d, err := store.GetEntryDetail(result.EntryID)
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Kind != EntryKindFact {
		t.Errorf("concept kind = %q, want overridden %q", d.Kind, EntryKindFact)
	}
}

// TestService_ConfirmAdd_InvalidKindOverrideRejected covers rejecting a bad
// override value at confirm time rather than silently defaulting it.
func TestService_ConfirmAdd_InvalidKindOverrideRejected(t *testing.T) {
	svc, store, db := setupService(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", EntryKindConcept,
		[]string{"p1"}, nil, AddEvidence{}, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryKind: "not-a-kind"}, nil); err == nil {
		t.Fatal("expected error for invalid kind override")
	}
}

// TestService_CreateManualCandidate_InvalidKindRejected covers the manual
// draft form's kind field validation.
func TestService_CreateManualCandidate_InvalidKindRejected(t *testing.T) {
	svc, _, _ := setupService(t)
	if _, err := svc.CreateManualCandidate("d1", "手动概念", "desc", "not-a-kind", nil); err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

// TestService_UpdateEntryMeta_PreservesKindWhenNotSpecified covers the
// plain metadata edit path: the edit modal's name/description fields don't
// currently resend kind, so an empty kind must keep the concept's existing
// classification rather than reset it to the concept default.
func TestService_UpdateEntryMeta_PreservesKindWhenNotSpecified(t *testing.T) {
	svc, store, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	if err := store.UpdateEntryMeta("c1", "c1", "", EntryKindFact); err != nil {
		t.Fatal(err)
	}

	if err := svc.UpdateEntryMeta("c1", "新名称", "新描述", ""); err != nil {
		t.Fatalf("update concept meta: %v", err)
	}
	d, err := store.GetEntryDetail("c1")
	if err != nil {
		t.Fatalf("get concept detail: %v", err)
	}
	if d.Kind != EntryKindFact {
		t.Errorf("kind = %q, want preserved %q", d.Kind, EntryKindFact)
	}
}

// TestService_UpdateEntryMeta_InvalidKindRejected covers rejecting a bad
// explicit kind value on the plain metadata edit path.
func TestService_UpdateEntryMeta_InvalidKindRejected(t *testing.T) {
	svc, _, db := setupService(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	if err := svc.UpdateEntryMeta("c1", "新名称", "新描述", "not-a-kind"); err == nil {
		t.Fatal("expected error for invalid kind")
	}
}
