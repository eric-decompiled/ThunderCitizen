// Package assets fingerprints files under static/ at boot so templates can
// emit cache-busted URLs (e.g. "/static/css/style.css?v=ab12cd34"). The
// browser cache is hard-set to a week-immutable on /static/* — without
// fingerprinting, deploys would not invalidate stale CSS/JS until the TTL
// expired. Each file's hash is the first 8 hex chars of SHA-256 over its
// contents, computed once at startup.
//
// Templates call assets.URL("/static/css/style.css"). Unknown paths fall
// through unchanged so missing-file bugs surface as 404s instead of silent
// rewrites.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu       sync.RWMutex
	manifest = map[string]string{}
)

// Init walks root, hashes every file, and stores the short hash keyed by
// the URL path ("/<urlPrefix>/...rel..."). Safe to call multiple times;
// each call replaces the prior manifest.
func Init(root, urlPrefix string) error {
	urlPrefix = strings.Trim(urlPrefix, "/")
	next := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip source maps and dotfiles — never referenced by templates.
		base := d.Name()
		if strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".map") {
			return nil
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := "/" + urlPrefix + "/" + filepath.ToSlash(rel)
		next[key] = hash
		return nil
	})
	if err != nil {
		return err
	}
	mu.Lock()
	manifest = next
	mu.Unlock()
	return nil
}

// URL returns path with a "?v=<hash>" suffix when the file is in the
// manifest, or the bare path otherwise. Unknown paths are returned as-is
// so 404s remain visible during development.
func URL(path string) string {
	mu.RLock()
	h, ok := manifest[path]
	mu.RUnlock()
	if !ok {
		return path
	}
	return path + "?v=" + h
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}
