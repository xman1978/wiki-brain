package answer

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func TestStore_SaveAndGet(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	r := &AnswerResult{
		AnswerID:  "a-001",
		Question:  "测试问题",
		Content:   "测试回答",
		Citations: []string{"f1", "f2"},
		HasAnswer: true,
		Path:      "short",
		EvidenceSet: &retrieval.EvidenceSet{
			Question: "测试问题",
			Path:     "short",
		},
	}

	if err := store.Save(r, "v1", "default"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("a-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Content != "测试回答" {
		t.Errorf("content: got %q", got.Content)
	}
	if !got.HasAnswer {
		t.Error("expected has_answer=true")
	}
	if len(got.Citations) != 2 {
		t.Errorf("citations: got %v", got.Citations)
	}
	if got.EvidenceSet == nil || got.EvidenceSet.Path != "short" {
		t.Error("evidence_set not deserialized correctly")
	}
}

func TestStore_GetNotFound(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	got, err := store.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent")
	}
}
