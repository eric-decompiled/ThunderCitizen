# keys/

Trust store for signed muni data bundles. Embedded into the server binary
at build time via `//go:embed` (see `embed.go`). Every release ships its
own snapshot of this directory — rotation and revocation are code
changes, not runtime config.

## Layout

```
keys/
  approved/     # trusted signers — one key per file, .pub extension
  revoked/      # explicitly distrusted — same format
```

Files are in `authorized_keys` format (the exact text
`ssh-keygen -L -f <key>` accepts). One key per file. Filename is
arbitrary but conventionally `<owner>-<device>-<year>.pub`, e.g.
`eric-yubikey-2026.pub`.

## Verification logic

On boot, `internal/munisign` loads both directories into a `Trust`
struct. A bundle's manifest.sig verifies only if:

1. The signer's SHA256 fingerprint is **not** present in `revoked/`.
2. The signer's SHA256 fingerprint **is** present in `approved/`.
3. The SSH signature blob verifies against the matched approved key.

A fingerprint that appears in both trees is a repo bug — `LoadTrust()`
refuses to start the server (fail-closed on ambiguous trust).

## Adding a signer

1. Export the public key in `authorized_keys` format.
   - YubiKey FIDO2 ECDSA: `ssh-keygen -L -f ~/.ssh/id_ecdsa_sk.pub`
   - ed25519: `ssh-keygen -y -f ~/.ssh/id_ed25519`
2. Save the single line to `keys/approved/<owner>-<device>-<year>.pub`.
3. Commit, release a new binary. The next bundle signed by that key
   will verify.

## Rotating a key

1. Add the new key to `approved/` (as above).
2. Sign the next bundle with the new key.
3. Optional grace period: leave the old key in `approved/` for a
   release or two while bundles signed by either key verify.
4. Once no live bundle is signed by the old key, delete it from
   `approved/` and commit.

## Revoking a compromised key

1. **Move** the key file from `approved/` to `revoked/` (or copy if it
   was never in `approved/` and you want a belt-and-braces ban).
2. Commit and cut a new release immediately.
3. Publish a fresh bundle signed by a different approved key.

From the moment the new binary boots, any bundle signed by the revoked
fingerprint is rejected — including bundles that were signed before the
revocation was committed. Trust is a property of the running binary,
not of the bundle's age.

## Fingerprint quick-reference

```
ssh-keygen -l -f keys/approved/<file>.pub
```

Prints the SHA256 fingerprint used to key this store.
