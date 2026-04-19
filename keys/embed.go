// Package keys holds the trust store for munisign bundle verification.
//
// Files in keys/approved/*.pub are treated as trusted signers. Files in
// keys/revoked/*.pub are explicitly distrusted — if a bundle is signed by
// a fingerprint that appears in revoked/, verification fails even if the
// same fingerprint also appears in approved/ (fail-closed on ambiguous
// trust).
//
// Both directories are committed to the repo and embedded into the
// server binary at build time, so rotation and revocation ship as part
// of the normal release flow. Moving a key file from approved/ to
// revoked/ and releasing a new binary retroactively invalidates every
// bundle that key ever signed, from the moment that binary boots.
//
// The directory layout intentionally mirrors ~/.ssh/authorized_keys:
// files are in authorized_keys format, one key per file. This keeps the
// option open to reuse the same store for SSH-based server access
// controls without restructuring later.
package keys

import "embed"

// Approved contains the trusted signer public keys. The `all:` prefix
// includes dotfiles so an empty directory seeded with `.gitkeep` still
// satisfies the embed directive.
//
//go:embed all:approved
var Approved embed.FS

// Revoked contains fingerprints (as .pub files) that must be rejected
// even if the same key is also present in Approved.
//
//go:embed all:revoked
var Revoked embed.FS
