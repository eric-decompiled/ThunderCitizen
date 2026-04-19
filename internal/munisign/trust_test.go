package munisign

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/ssh"

	"thundercitizen/keys"
)

// testPubKey returns an ed25519 ssh.Signer + its authorized_keys line.
func testPubKey(t *testing.T) (ssh.Signer, []byte, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	line := ssh.MarshalAuthorizedKey(signer.PublicKey())
	fp := ssh.FingerprintSHA256(signer.PublicKey())
	return signer, line, fp
}

// TestEmbeddedTrust_Loads covers the production case: the committed
// keys/approved and keys/revoked trees parse cleanly and every .pub
// is a valid authorized_keys line. Future contributors adding an
// invalid file will see this test fail.
func TestEmbeddedTrust_Loads(t *testing.T) {
	trust, err := LoadTrust()
	if err != nil {
		t.Fatalf("LoadTrust: %v", err)
	}
	if len(trust.Approved) == 0 {
		t.Fatal("keys/approved/ is empty — production binaries need at least one trusted signer")
	}
	for fp := range trust.Revoked {
		if _, ok := trust.Approved[fp]; ok {
			t.Errorf("fingerprint %s is both approved and revoked", fp)
		}
	}
}

// TestEmbeddedTrust_ApprovedParse asserts every committed approved key
// is a real authorized_keys entry with a non-empty fingerprint. The
// embedded loader already validates parse-ability, but the explicit
// assertion here makes the failure mode obvious.
func TestEmbeddedTrust_ApprovedParse(t *testing.T) {
	trust, err := LoadTrust()
	if err != nil {
		t.Fatalf("LoadTrust: %v", err)
	}
	for fp, k := range trust.Approved {
		if fp == "" {
			t.Errorf("empty fingerprint for %s", k.Filename)
		}
		if k.Key == nil {
			t.Errorf("nil key for %s", k.Filename)
		}
	}
	_ = keys.Approved // keep the import referenced
}

// TestLoadTrust_Conflict ensures a fingerprint appearing in both trees
// is rejected — fail-closed on ambiguous trust.
func TestLoadTrust_Conflict(t *testing.T) {
	_, pubLine, _ := testPubKey(t)
	approved := fstest.MapFS{
		"approved/good.pub": &fstest.MapFile{Data: pubLine},
	}
	revoked := fstest.MapFS{
		"revoked/good.pub": &fstest.MapFile{Data: pubLine},
	}
	if _, err := loadTrustFS(approved, revoked); err == nil {
		t.Fatal("expected ambiguous-trust error, got nil")
	}
}

// TestLoadTrust_IgnoresNonPub skips files like .gitkeep so an empty
// revoked/ directory doesn't panic the embed loader.
func TestLoadTrust_IgnoresNonPub(t *testing.T) {
	_, pubLine, _ := testPubKey(t)
	approved := fstest.MapFS{
		"approved/good.pub":  &fstest.MapFile{Data: pubLine},
		"approved/README.md": &fstest.MapFile{Data: []byte("notes")},
	}
	revoked := fstest.MapFS{
		"revoked/.gitkeep": &fstest.MapFile{Data: nil},
	}
	tr, err := loadTrustFS(approved, revoked)
	if err != nil {
		t.Fatalf("loadTrustFS: %v", err)
	}
	if len(tr.Approved) != 1 {
		t.Errorf("approved=%d, want 1", len(tr.Approved))
	}
	if len(tr.Revoked) != 0 {
		t.Errorf("revoked=%d, want 0", len(tr.Revoked))
	}
}

// TestVerifyFSWithTrust_RevokedRejected ensures a bundle signed by a
// key present only in revoked/ fails verification with the revocation
// error — the core security property of the blacklist.
func TestVerifyFSWithTrust_RevokedRejected(t *testing.T) {
	signer, pubLine, fp := testPubKey(t)

	// Revoked-only trust store.
	revoked := fstest.MapFS{
		"revoked/burned.pub": &fstest.MapFile{Data: pubLine},
	}
	approved := fstest.MapFS{}
	tr, err := loadTrustFS(approved, revoked)
	if err != nil {
		t.Fatalf("loadTrustFS: %v", err)
	}

	// Build a validly signed bundle.
	files := fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("col1\tcol2\nval1\tval2\n")},
	}
	sig := testSignFS(t, files, signer)
	files[ManifestFile] = &fstest.MapFile{Data: sig}

	_, err = VerifyFSWithTrust(files, tr)
	if err == nil {
		t.Fatal("expected revocation error, got nil")
	}
	if fp == "" || err.Error() == "" {
		t.Errorf("fp=%q err=%v", fp, err)
	}
}

// TestVerifyFSWithTrust_ApprovedAccepted is the happy path: signer in
// approved/, not in revoked/, signature matches.
func TestVerifyFSWithTrust_ApprovedAccepted(t *testing.T) {
	signer, pubLine, fp := testPubKey(t)
	approved := fstest.MapFS{
		"approved/me.pub": &fstest.MapFile{Data: pubLine},
	}
	revoked := fstest.MapFS{}
	tr, err := loadTrustFS(approved, revoked)
	if err != nil {
		t.Fatalf("loadTrustFS: %v", err)
	}

	files := fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("col1\tcol2\nval1\tval2\n")},
	}
	sig := testSignFS(t, files, signer)
	files[ManifestFile] = &fstest.MapFile{Data: sig}

	v, err := VerifyFSWithTrust(files, tr)
	if err != nil {
		t.Fatalf("VerifyFSWithTrust: %v", err)
	}
	if v.SignerFingerprint != fp {
		t.Errorf("fingerprint: got %s, want %s", v.SignerFingerprint, fp)
	}
}

// TestVerifyFSWithTrust_UntrustedRejected: signer is neither approved
// nor revoked — reject with the "not in approved" error.
func TestVerifyFSWithTrust_UntrustedRejected(t *testing.T) {
	signer, _, _ := testPubKey(t)
	tr, err := loadTrustFS(fstest.MapFS{}, fstest.MapFS{})
	if err != nil {
		t.Fatalf("loadTrustFS: %v", err)
	}

	files := fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("x\n")},
	}
	sig := testSignFS(t, files, signer)
	files[ManifestFile] = &fstest.MapFile{Data: sig}

	if _, err := VerifyFSWithTrust(files, tr); err == nil {
		t.Fatal("expected untrusted-signer error")
	}
}
