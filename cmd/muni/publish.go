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
	signingKeyFile = ".signing-key.pub"
	indexKey       = "index.json"
)

func runPublish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	dir := fs.String("dir", "data/muni", "bundle directory (must contain manifest.sig)")
	dryRun := fs.Bool("dry-run", false, "build the zip + index but don't upload")
	if err := fs.Parse(args); err != nil {
		fail("flags: %v", err)
	}

	// 1. Verify the bundle. Reuses the same signature check the server does,
	//    so anything that fails here would fail in production too.
	pubKey, err := os.ReadFile(signingKeyFile)
	if err != nil {
		fail("read %s: %v (run from repo root)", signingKeyFile, err)
	}
	v, err := munisign.VerifyFS(os.DirFS(*dir), pubKey)
	if err != nil {
		fail("verify %s: %v (run `munisign sign` first)", *dir, err)
	}
	logf("verified  merkle=%s  signer=%s", v.MerkleRoot[:12], v.SignerFingerprint)

	// 2. Build the versioned zip. Name is content-addressed so old bundles
	//    remain fetchable for audit/rollback.
	date := time.Now().UTC().Format("2006-01-02")
	versioned := fmt.Sprintf("%s-muni-%s.zip", date, v.MerkleRoot[:12])
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
		PublishedAt:       time.Now().UTC().Format(time.RFC3339),
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
	if err := putPublic(ctx, client, versioned, zipPath, "application/zip"); err != nil {
		fail("upload bundle: %v", err)
	}
	logf("uploaded  %s/%s", spacesBucket, versioned)

	if err := putPublic(ctx, client, indexKey, indexPath, "application/json"); err != nil {
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
func putPublic(ctx context.Context, c *minio.Client, key, src, ctype string) error {
	_, err := c.FPutObject(ctx, spacesBucket, key, src, minio.PutObjectOptions{
		ContentType: ctype,
		UserMetadata: map[string]string{
			// DO Spaces accepts the x-amz-acl header verbatim.
			"x-amz-acl": "public-read",
		},
	})
	return err
}

// headOK confirms a public URL returns 200.
func headOK(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return nil
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

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src, err := os.Open(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return err
		}
		dst, err := w.Create(e.Name())
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			return err
		}
		src.Close()
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
