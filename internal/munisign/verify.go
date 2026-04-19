package munisign

import (
	"fmt"
	"io/fs"

	"golang.org/x/crypto/ssh"
)

const ManifestFile = "manifest.sig"
const Namespace = "muni"

// Verification holds the result of a successful signature verification.
type Verification struct {
	MerkleRoot        string // hex-encoded SHA-256 root hash
	SignerKey         string // key type, e.g. "sk-ecdsa-sha2-nistp256@openssh.com"
	SignerFingerprint string // SHA256 fingerprint of the signing key
}

// VerifyFS reads manifest.sig from fsys, computes the Merkle root of all
// other files, and verifies the SSH signature against pubKey.
//
// pubKey is in authorized_keys format:
//
//	"sk-ecdsa-sha2-nistp256@openssh.com AAAA... comment"
//
// Returns nil if the signature is valid. Prefer VerifyFSWithTrust in
// production code paths — it enforces the embedded keys/approved +
// keys/revoked trust store. This single-key entry point is kept for
// test harnesses and one-shot tools.
func VerifyFS(fsys fs.FS, pubKey []byte) (*Verification, error) {
	sigData, err := fs.ReadFile(fsys, ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("munisign: reading %s: %w", ManifestFile, err)
	}

	sig, err := parseSSHSig(sigData)
	if err != nil {
		return nil, fmt.Errorf("munisign: parsing %s: %w", ManifestFile, err)
	}

	if sig.Namespace != Namespace {
		return nil, fmt.Errorf("munisign: namespace %q, want %q", sig.Namespace, Namespace)
	}

	rootHash, err := HashFS(fsys, map[string]bool{ManifestFile: true})
	if err != nil {
		return nil, fmt.Errorf("munisign: hashing files: %w", err)
	}

	blob := signedData(sig.Namespace, sig.HashAlg, []byte(rootHash))

	expectedKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("munisign: parsing public key: %w", err)
	}

	if err := expectedKey.Verify(blob, sig.Signature); err != nil {
		return nil, fmt.Errorf("munisign: signature verification failed: %w", err)
	}

	return &Verification{
		MerkleRoot:        rootHash,
		SignerKey:         sig.PublicKey.Type(),
		SignerFingerprint: ssh.FingerprintSHA256(sig.PublicKey),
	}, nil
}
