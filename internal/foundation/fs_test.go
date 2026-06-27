package foundation

import (
	"os"
	"testing"
)

func TestEnsureDirectories(t *testing.T) {
	base := t.TempDir()

	if err := EnsureDirectories(base); err != nil {
		t.Fatalf("EnsureDirectories failed: %v", err)
	}

	for _, dir := range requiredDirs {
		path := base + "/" + dir
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("directory %s not created: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestEnsureDirectoriesIdempotent(t *testing.T) {
	base := t.TempDir()

	if err := EnsureDirectories(base); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureDirectories(base); err != nil {
		t.Fatalf("second call: %v", err)
	}
}
