package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"thundercitizen/internal/config"
	"thundercitizen/internal/muni"
	"thundercitizen/internal/munisign"
)

const (
	spacesEndpoint = "tor1.digitaloceanspaces.com"
	spacesBucket   = "thundercitizen"
	publicBase     = "https://thundercitizen.tor1.digitaloceanspaces.com"
	indexKey       = "index.json"
)

func runPublish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	dir := fs.String("dir", "data/muni", "bundle directory (must contain manifest.sig)")
	dryRun := fs.Bool("dry-run", false, "build the zip + index but don't upload")
	if err := fs.Parse(args); err != nil {
		fail("flags: %v", err)
	}

	// 1. Verify the bundle against the embedded trust store. Anything
	//    that fails here would fail on the server too — publishing a
	//    bundle signed by a revoked key would just get rejected on
	//    the next apply cycle, so catch it locally first.
	trust, err := munisign.LoadTrust()
	if err != nil {
		fail("load trust: %v", err)
	}
	v, err := munisign.VerifyFSWithTrust(os.DirFS(*dir), trust)
	if err != nil {
		fail("verify %s: %v (run `munisign sign` first, or check keys/approved/)", *dir, err)
	}
	logf("verified  merkle=%s  signer=%s", v.MerkleRoot[:12], v.SignerFingerprint)

	// 2. Build the versioned zip. Name is content-addressed so old bundles
	//    remain fetchable for audit/rollback.
	now := time.Now().UTC()
	versioned := fmt.Sprintf("%s-muni-%s.zip", now.Format("2006-01-02"), v.MerkleRoot[:12])
	zipPath := filepath.Join("data", versioned)
	if err := os.MkdirAll("data", 0o755); err != nil {
		fail("mkdir data: %v", err)
	}
	if err := zipFlat(*dir, zipPath); err != nil {
		fail("zip: %v", err)
	}
	stat, _ := os.Stat(zipPath)
	logf("zipped  path=%s  size=%d", zipPath, stat.Size())

	// 3. Write the pointer file. The server fetches this first to discover
	//    the current bundle name and declared schema version.
	idx := muni.Index{
		SchemaVersion:     muni.SchemaVersion,
		Bundle:            versioned,
		MerkleRoot:        v.MerkleRoot,
		SignerFingerprint: v.SignerFingerprint,
		PublishedAt:       now.Format(time.RFC3339),
	}
	indexPath := filepath.Join("data", "index.json")
	if err := writeJSON(indexPath, idx); err != nil {
		fail("write index: %v", err)
	}
	logf("indexed  schema=%d  bundle=%s", idx.SchemaVersion, idx.Bundle)

	if *dryRun {
		logf("dry run — skipping upload")
		return
	}

	// 4. Resolve credentials. config.Secret reads env first, then
	//    secrets.conf, then a fallback. We treat empty as "not set" and
	//    fail loudly before hitting DO Spaces with anonymous requests.
	accessKey := config.Secret("AWS_ACCESS_KEY_ID", "")
	secretKey := config.Secret("AWS_SECRET_ACCESS_KEY", "")
	if accessKey == "" || secretKey == "" {
		fail(`missing DO Spaces credentials
  set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY in the environment
  or add them to secrets.conf (gitignored):
    AWS_ACCESS_KEY_ID=...
    AWS_SECRET_ACCESS_KEY=...
  generate keys at: https://cloud.digitalocean.com/account/api/spaces`)
	}

	client, err := minio.New(spacesEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true,
	})
	if err != nil {
		fail("s3 client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Order matters: upload the immutable bundle first, then swap the
	// pointer. A server fetching between the two calls sees either the
	// previous complete state or the new complete state, never a broken
	// index pointing at a missing bundle.
	// Bundle is content-addressed — safe to cache forever.
	if err := putPublic(ctx, client, versioned, zipPath, "application/zip", "public, max-age=31536000, immutable"); err != nil {
		fail("upload bundle: %v", err)
	}
	logf("uploaded  %s/%s", spacesBucket, versioned)

	// Index is the mutable pointer — keep CDN/browser revalidation tight.
	if err := putPublic(ctx, client, indexKey, indexPath, "application/json", "public, max-age=60, must-revalidate"); err != nil {
		fail("upload index: %v", err)
	}
	logf("uploaded  %s/%s", spacesBucket, indexKey)

	// 5. Confirm via HEAD. Catches permission/ACL bugs that the S3 API
	//    silently allowed but actually didn't make public.
	bundleURL := publicBase + "/" + versioned
	indexURL := publicBase + "/" + indexKey
	if err := headOK(ctx, bundleURL); err != nil {
		fail("verify bundle URL: %v", err)
	}
	if err := headOK(ctx, indexURL); err != nil {
		fail("verify index URL: %v", err)
	}

	logf("live:")
	logf("  bundle: %s", bundleURL)
	logf("  index:  %s", indexURL)
}

// putPublic uploads a file to DO Spaces with public-read ACL.
func putPublic(ctx context.Context, c *minio.Client, key, src, ctype, cacheControl string) error {
	_, err := c.FPutObject(ctx, spacesBucket, key, src, minio.PutObjectOptions{
		ContentType:  ctype,
		CacheControl: cacheControl,
		// minio-go's isAmzHeader passes x-amz-acl through from UserMetadata
		// as a raw canned-ACL header (not x-amz-meta-*).
		UserMetadata: map[string]string{"x-amz-acl": "public-read"},
	})
	return err
}

// headOK confirms a public URL returns 200. Retries once to smooth over
// DO Spaces edge-cache propagation right after an upload.
func headOK(ctx context.Context, url string) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return lastErr
}

// zipFlat writes a flat zip of every regular file in srcDir (no nested paths).
func zipFlat(srcDir, outPath string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	w := zip.NewWriter(out)
	defer w.Close()

	copyOne := func(name string) error {
		src, err := os.Open(filepath.Join(srcDir, name))
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := w.Create(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, src)
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyOne(e.Name()); err != nil {
			return err
		}
	}
	return nil
}

// writeJSON pretty-prints a value to a file, with a trailing newline so
// humans can `cat` it without the shell prompt sticking to the last byte.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
