package munisign

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"

	"thundercitizen/keys"
)

// TrustedKey is one entry from keys/approved/ or keys/revoked/.
type TrustedKey struct {
	Filename    string // e.g. "eric-yubikey-2026.pub"
	Fingerprint string // SHA256 fingerprint, e.g. "SHA256:abc..."
	Key         ssh.PublicKey
}

// Trust is the set of approved and revoked signers loaded from the
// embedded keys/ directory at binary build time.
type Trust struct {
	Approved map[string]TrustedKey // fingerprint -> key
	Revoked  map[string]TrustedKey // fingerprint -> key
}

// LoadTrust parses the embedded approved and revoked trees. Returns an
// error if any file fails to parse as an authorized_keys entry, or if
// the same fingerprint appears in both trees (ambiguous trust).
func LoadTrust() (*Trust, error) {
	return loadTrustFS(keys.Approved, keys.Revoked)
}

func loadTrustFS(approvedFS, revokedFS fs.FS) (*Trust, error) {
	approved, err := readKeys(approvedFS, "approved")
	if err != nil {
		return nil, err
	}
	revoked, err := readKeys(revokedFS, "revoked")
	if err != nil {
		return nil, err
	}
	for fp, rk := range revoked {
		if ak, ok := approved[fp]; ok {
			return nil, fmt.Errorf("munisign: fingerprint %s appears in both keys/approved/%s and keys/revoked/%s — ambiguous trust, refusing to start", fp, ak.Filename, rk.Filename)
		}
	}
	return &Trust{Approved: approved, Revoked: revoked}, nil
}

// readKeys walks fsys under root/, parsing every .pub file as an
// authorized_keys entry. A missing root directory is treated as empty.
func readKeys(fsys fs.FS, root string) (map[string]TrustedKey, error) {
	out := make(map[string]TrustedKey)
	if _, err := fs.Stat(fsys, root); err != nil {
		return out, nil
	}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".pub") {
			return nil // skip .gitkeep etc
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		fp := ssh.FingerprintSHA256(key)
		if existing, ok := out[fp]; ok {
			return fmt.Errorf("%s and %s share fingerprint %s — keep only one", existing.Filename, path.Base(p), fp)
		}
		out[fp] = TrustedKey{
			Filename:    path.Base(p),
			Fingerprint: fp,
			Key:         key,
		}
		return nil
	})
	if err != nil {
		// The embed FS reports fs.ErrNotExist when the root directory
		// itself is missing, which should never happen for a compiled
		// binary — surface it clearly.
		return nil, fmt.Errorf("munisign: reading %s/: %w", root, err)
	}
	return out, nil
}

// checkRevoked returns an error if fp is on the blacklist.
func (t *Trust) checkRevoked(fp string) error {
	if rk, ok := t.Revoked[fp]; ok {
		return fmt.Errorf("munisign: signer %s is revoked (keys/revoked/%s)", fp, rk.Filename)
	}
	return nil
}

// match returns the approved key for fp, or an error if the signer is
// not trusted.
func (t *Trust) match(fp string) (TrustedKey, error) {
	if err := t.checkRevoked(fp); err != nil {
		return TrustedKey{}, err
	}
	k, ok := t.Approved[fp]
	if !ok {
		return TrustedKey{}, fmt.Errorf("munisign: signer %s is not in keys/approved/", fp)
	}
	return k, nil
}

// VerifyFSWithTrust verifies a bundle using the trust store rather than
// a single expected public key. The signer must be approved and not
// revoked. Returns a Verification with the matched trust entry.
func VerifyFSWithTrust(fsys fs.FS, t *Trust) (*Verification, error) {
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

	fp := ssh.FingerprintSHA256(sig.PublicKey)
	trusted, err := t.match(fp)
	if err != nil {
		return nil, err
	}

	rootHash, err := HashFS(fsys, map[string]bool{ManifestFile: true})
	if err != nil {
		return nil, fmt.Errorf("munisign: hashing files: %w", err)
	}
	blob := signedData(sig.Namespace, sig.HashAlg, []byte(rootHash))

	if err := trusted.Key.Verify(blob, sig.Signature); err != nil {
		return nil, fmt.Errorf("munisign: signature verification failed: %w", err)
	}

	return &Verification{
		MerkleRoot:        rootHash,
		SignerKey:         sig.PublicKey.Type(),
		SignerFingerprint: fp,
	}, nil
}

// Summary returns sorted slices of approved and revoked keys suitable
// for display on the admin page. No key material is included beyond
// the filename and fingerprint.
func (t *Trust) Summary() (approved []TrustedKey, revoked []TrustedKey) {
	approved = make([]TrustedKey, 0, len(t.Approved))
	for _, k := range t.Approved {
		approved = append(approved, k)
	}
	revoked = make([]TrustedKey, 0, len(t.Revoked))
	for _, k := range t.Revoked {
		revoked = append(revoked, k)
	}
	sortByFilename(approved)
	sortByFilename(revoked)
	return
}

// compile-time check that embed.FS satisfies fs.FS (helps the
// readKeys signature read cleanly).
var _ fs.FS = embed.FS{}

func sortByFilename(ks []TrustedKey) {
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j-1].Filename > ks[j].Filename; j-- {
			ks[j-1], ks[j] = ks[j], ks[j-1]
		}
	}
}
