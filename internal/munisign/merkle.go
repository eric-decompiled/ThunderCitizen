// Package munisign provides Merkle tree hashing and SSH signature
// verification for signed data bundles. It implements the "muni" signing
// convention: a flat directory of files is hashed into a single Merkle
// root, which is then signed with an SSH key using the SSHSIG format
// (the same format ssh-keygen -Y sign produces).
package munisign

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"sort"
)

// HashFS computes a deterministic Merkle root of all files in fsys.
// Files whose names appear in skip are excluded (used to exclude
// manifest.sig during verification).
//
// Algorithm:
//  1. List files via fs.ReadDir("."), sort lexically, skip entries in skip.
//  2. Per file: leaf = SHA256("leaf:" + name + "\n" + fileContent).
//  3. Root = SHA256("root:\n" + hex(leaf1) + "\n" + hex(leaf2) + "\n" + ...).
//
// Domain-separated prefixes ("leaf:", "root:") prevent second-preimage
// attacks. Returns lowercase hex-encoded SHA-256.
func HashFS(fsys fs.FS, skip map[string]bool) (string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return "", fmt.Errorf("munisign: read dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if skip != nil && skip[e.Name()] {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	if len(names) == 0 {
		return "", fmt.Errorf("munisign: no files to hash")
	}

	leaves := make([]string, 0, len(names))
	for _, name := range names {
		h, err := hashLeaf(fsys, name)
		if err != nil {
			return "", err
		}
		leaves = append(leaves, h)
	}

	root := sha256.New()
	root.Write([]byte("root:\n"))
	for _, leaf := range leaves {
		root.Write([]byte(leaf))
		root.Write([]byte("\n"))
	}

	return hex.EncodeToString(root.Sum(nil)), nil
}

// hashLeaf computes SHA256("leaf:" + name + "\n" + content) for one file.
func hashLeaf(fsys fs.FS, name string) (string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return "", fmt.Errorf("munisign: open %s: %w", name, err)
	}
	defer f.Close()

	h := sha256.New()
	h.Write([]byte("leaf:" + name + "\n"))
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("munisign: read %s: %w", name, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
