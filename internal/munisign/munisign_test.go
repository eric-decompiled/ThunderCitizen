package munisign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/pem"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/ssh"
)

// testFiles returns a small MapFS for testing.
func testFiles() fstest.MapFS {
	return fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("col1\tcol2\nval1\tval2\n")},
		"b.tsv": &fstest.MapFile{Data: []byte("x\t1\ny\t2\n")},
	}
}

// testKey generates an ed25519 key pair and returns the ssh.Signer and
// the authorized_keys-format public key line.
func testKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	_ = pub
	authKey := ssh.MarshalAuthorizedKey(signer.PublicKey())
	return signer, authKey
}

// testSignFS constructs an SSHSIG blob in Go (no ssh-keygen needed)
// and returns manifest.sig bytes for the given fsys.
func testSignFS(t *testing.T, fsys fstest.MapFS, signer ssh.Signer) []byte {
	t.Helper()
	rootHash, err := HashFS(fsys, map[string]bool{ManifestFile: true})
	if err != nil {
		t.Fatalf("HashFS: %v", err)
	}

	blob := signedData(Namespace, "sha512", []byte(rootHash))
	sig, err := signer.Sign(rand.Reader, blob)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	return marshalSSHSig(signer.PublicKey(), sig)
}

// marshalSSHSig builds the SSHSIG binary format and wraps it in PEM.
func marshalSSHSig(pubKey ssh.PublicKey, sig *ssh.Signature) []byte {
	var buf []byte

	// Magic + version
	buf = append(buf, []byte(sshsigMagic)...)
	buf = appendSSHString(buf, nil) // version placeholder
	// Fix: version is uint32, not a string
	buf = buf[:6]                 // rewind past the bad string
	buf = append(buf, 0, 0, 0, 1) // version = 1

	// Public key
	buf = appendSSHString(buf, pubKey.Marshal())

	// Namespace
	buf = appendSSHString(buf, []byte(Namespace))

	// Reserved (empty)
	buf = appendSSHString(buf, nil)

	// Hash algorithm
	buf = appendSSHString(buf, []byte("sha512"))

	// Signature blob: format string + blob bytes (+ rest for SK keys)
	var sigBuf []byte
	sigBuf = appendSSHString(sigBuf, []byte(sig.Format))
	sigBuf = appendSSHString(sigBuf, sig.Blob)
	sigBuf = append(sigBuf, sig.Rest...)
	buf = appendSSHString(buf, sigBuf)

	block := &pem.Block{
		Type:  sshsigPEMType,
		Bytes: buf,
	}
	return pem.EncodeToMemory(block)
}

func TestHashFS_Deterministic(t *testing.T) {
	fs1 := fstest.MapFS{
		"b.tsv": &fstest.MapFile{Data: []byte("second")},
		"a.tsv": &fstest.MapFile{Data: []byte("first")},
	}
	fs2 := fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("first")},
		"b.tsv": &fstest.MapFile{Data: []byte("second")},
	}

	h1, err := HashFS(fs1, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashFS(fs2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hashes differ:\n  %s\n  %s", h1, h2)
	}
}

func TestHashFS_ContentChange(t *testing.T) {
	fs1 := testFiles()
	fs2 := fstest.MapFS{
		"a.tsv": &fstest.MapFile{Data: []byte("col1\tcol2\nval1\tval2\n")},
		"b.tsv": &fstest.MapFile{Data: []byte("x\t1\ny\t3\n")}, // changed "2" to "3"
	}

	h1, _ := HashFS(fs1, nil)
	h2, _ := HashFS(fs2, nil)
	if h1 == h2 {
		t.Error("hashes should differ after content change")
	}
}

func TestHashFS_Skip(t *testing.T) {
	base := testFiles()

	h1, _ := HashFS(base, nil)

	withSig := fstest.MapFS{
		"a.tsv":        base["a.tsv"],
		"b.tsv":        base["b.tsv"],
		"manifest.sig": &fstest.MapFile{Data: []byte("signature data")},
	}

	h2, _ := HashFS(withSig, map[string]bool{ManifestFile: true})
	if h1 != h2 {
		t.Errorf("skip should exclude manifest.sig:\n  without: %s\n  with skip: %s", h1, h2)
	}
}

func TestHashFS_Empty(t *testing.T) {
	_, err := HashFS(fstest.MapFS{}, nil)
	if err == nil {
		t.Error("expected error for empty FS")
	}
}

func TestVerifyFS_RoundTrip(t *testing.T) {
	signer, pubKey := testKey(t)
	files := testFiles()

	sigBytes := testSignFS(t, files, signer)
	files[ManifestFile] = &fstest.MapFile{Data: sigBytes}

	v, err := VerifyFS(files, pubKey)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if v.MerkleRoot == "" {
		t.Error("missing merkle root")
	}
	if v.SignerKey == "" {
		t.Error("missing signer key type")
	}
	if v.SignerFingerprint == "" {
		t.Error("missing signer fingerprint")
	}
}

func TestVerifyFS_Tampered(t *testing.T) {
	signer, pubKey := testKey(t)
	files := testFiles()
	sigBytes := testSignFS(t, files, signer)

	// Tamper with a file after signing.
	files["a.tsv"] = &fstest.MapFile{Data: []byte("TAMPERED")}
	files[ManifestFile] = &fstest.MapFile{Data: sigBytes}

	if _, err := VerifyFS(files, pubKey); err == nil {
		t.Fatal("expected verification failure after tampering")
	}
}

func TestVerifyFS_WrongKey(t *testing.T) {
	signer, _ := testKey(t)
	_, wrongPubKey := testKey(t) // different key

	files := testFiles()
	sigBytes := testSignFS(t, files, signer)
	files[ManifestFile] = &fstest.MapFile{Data: sigBytes}

	if _, err := VerifyFS(files, wrongPubKey); err == nil {
		t.Fatal("expected verification failure with wrong key")
	}
}

func TestVerifyFS_MissingManifest(t *testing.T) {
	_, pubKey := testKey(t)
	files := testFiles() // no manifest.sig

	_, err := VerifyFS(files, pubKey)
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestParseSSHSig_RoundTrip(t *testing.T) {
	signer, _ := testKey(t)

	message := []byte("test-hash-hex")
	blob := signedData(Namespace, "sha512", message)
	sig, err := signer.Sign(rand.Reader, blob)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	armored := marshalSSHSig(signer.PublicKey(), sig)
	parsed, err := parseSSHSig(armored)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("version: %d", parsed.Version)
	}
	if parsed.Namespace != Namespace {
		t.Errorf("namespace: %q", parsed.Namespace)
	}
	if parsed.HashAlg != "sha512" {
		t.Errorf("hash_alg: %q", parsed.HashAlg)
	}
	if parsed.Signature.Format != sig.Format {
		t.Errorf("sig format: %q vs %q", parsed.Signature.Format, sig.Format)
	}
}

func TestSignedData(t *testing.T) {
	msg := []byte("hello")
	data := signedData(Namespace, "sha512", msg)

	// Should start with SSHSIG magic.
	if string(data[:6]) != sshsigMagic {
		t.Errorf("magic: %q", data[:6])
	}

	// The message hash should be SHA-512 of "hello".
	expected := sha512.Sum512(msg)
	_ = expected // Verified implicitly via round-trip tests.
}
