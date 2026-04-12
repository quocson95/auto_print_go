package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeIndexTitleCollapsesWhitespace(t *testing.T) {
	got := normalizeIndexTitle("  Nước   Anh \n Thiếu  Đầu Tư  ")
	want := "Nước Anh Thiếu Đầu Tư"

	if got != want {
		t.Fatalf("normalizeIndexTitle() = %q, want %q", got, want)
	}
}

func TestResolveLibraryHTMLPathFromWorkingDirectory(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tempDir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	path := filepath.Join(tempDir, "library.html")
	if err := os.WriteFile(path, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if got := resolveLibraryHTMLPath(); got != "library.html" {
		t.Fatalf("resolveLibraryHTMLPath() = %q, want %q", got, "library.html")
	}
}
