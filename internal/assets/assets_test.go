package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitAndURL(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "css", "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "css", "style.css.map"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dir, "/static"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got := URL("/static/css/style.css")
	if !strings.HasPrefix(got, "/static/css/style.css?v=") {
		t.Errorf("expected fingerprinted URL, got %q", got)
	}
	if len(strings.TrimPrefix(got, "/static/css/style.css?v=")) != 8 {
		t.Errorf("expected 8-char hash, got %q", got)
	}

	// Source maps must not be in the manifest.
	if mapped := URL("/static/css/style.css.map"); mapped != "/static/css/style.css.map" {
		t.Errorf("source maps must pass through, got %q", mapped)
	}

	// Unknown paths fall through unchanged.
	if mapped := URL("/static/missing.js"); mapped != "/static/missing.js" {
		t.Errorf("unknown paths must pass through, got %q", mapped)
	}

	// Hash changes when content changes.
	first := URL("/static/css/style.css")
	if err := os.WriteFile(filepath.Join(dir, "css", "style.css"), []byte("body{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "/static"); err != nil {
		t.Fatal(err)
	}
	second := URL("/static/css/style.css")
	if first == second {
		t.Errorf("hash should change with content: %q vs %q", first, second)
	}
}
