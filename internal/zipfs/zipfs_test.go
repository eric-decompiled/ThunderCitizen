package zipfs_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"thundercitizen/internal/zipfs"
)

// makeZip builds an in-memory zip containing the given filename→content pairs.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestFetch_OK(t *testing.T) {
	want := map[string]string{
		"hello.sql": "SELECT 1;",
		"world.sql": "SELECT 2;",
	}
	zipData := makeZip(t, want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipData)
	}))
	defer srv.Close()

	fsys, err := zipfs.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for name, wantBody := range want {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Errorf("ReadFile(%s): %v", name, err)
			continue
		}
		if string(data) != wantBody {
			t.Errorf("ReadFile(%s) = %q, want %q", name, data, wantBody)
		}
	}
}

func TestFetch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := zipfs.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestFetch_BadZip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a zip"))
	}))
	defer srv.Close()

	_, err := zipfs.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for corrupt zip, got nil")
	}
}

func TestFetch_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(makeZip(t, map[string]string{"a.sql": "SELECT 1;"}))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := zipfs.Fetch(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
