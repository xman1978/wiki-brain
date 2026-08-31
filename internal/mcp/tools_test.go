package mcp

import (
	"context"
	"strings"
	"testing"
)

// These validation branches return before touching s.sourceSvc, so a
// zero-value Server is enough to exercise them without a real DB/service.
func TestImportFileValidation(t *testing.T) {
	s := &Server{}
	ctx := context.Background()

	cases := []struct {
		name    string
		in      ImportFileInput
		wantErr string
	}{
		{
			name:    "neither provided",
			in:      ImportFileInput{},
			wantErr: "file_path or content_base64 is required",
		},
		{
			name:    "both provided",
			in:      ImportFileInput{FilePath: "/tmp/a.md", ContentBase64: "aGVsbG8="},
			wantErr: "mutually exclusive",
		},
		{
			name:    "content without filename",
			in:      ImportFileInput{ContentBase64: "aGVsbG8="},
			wantErr: "filename is required",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := s.importFile(ctx, nil, c.in)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestRetrieveRequiresQuestion(t *testing.T) {
	s := &Server{}
	_, _, err := s.retrieve(context.Background(), nil, RetrieveInput{Question: "  "})
	if err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("err = %v, want question is required", err)
	}
}
