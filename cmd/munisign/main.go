// Command munisign provides Merkle tree hashing, SSH signing, and
// verification for muni data bundles.
//
// Usage:
//
//	go run ./cmd/munisign hash  <dir>
//	go run ./cmd/munisign sign  -key <private_key> <dir>
//	go run ./cmd/munisign verify -key <public_key> <dir>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"thundercitizen/internal/munisign"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "hash":
		runHash(os.Args[2:])
	case "sign":
		runSign(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	default:
		usage()
	}
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func runHash(args []string) {
	if len(args) != 1 {
		fail("usage: munisign hash <dir>")
	}
	dir := args[0]

	hash, err := munisign.HashFS(os.DirFS(dir), map[string]bool{munisign.ManifestFile: true})
	if err != nil {
		fail("hash: %v", err)
	}
	fmt.Println(hash)
}

func runSign(args []string) {
	keyPath, dir := parseSignArgs(args)

	sig, err := munisign.SignFS(dir, keyPath, logf)
	if err != nil {
		fail("%v", err)
	}

	outPath := filepath.Join(dir, munisign.ManifestFile)
	if err := os.WriteFile(outPath, sig, 0o644); err != nil {
		fail("write %s: %v", outPath, err)
	}
	logf("wrote %s\n", outPath)

	promptUpload(dir)
}

// parseSignArgs supports three forms:
//
//	munisign sign <dir>              — autodetect from keys/approved/
//	munisign sign -key <k> <dir>     — explicit private key path
//	munisign sign -key <k>           — (error) dir required
//
// Autodetect walks ~/.ssh/*.pub, matches each fingerprint against the
// embedded trust store, and picks the private key next to the first
// match. Logs which key it chose so nothing is silently ambiguous.
func parseSignArgs(args []string) (string, string) {
	if len(args) >= 3 && args[0] == "-key" {
		return args[1], args[2]
	}
	if len(args) == 1 {
		keyPath, err := autodetectSigningKey()
		if err != nil {
			fail("%v\n\n  pass -key <privkey> to be explicit, or drop your approved signer's public key into keys/approved/ so autodetect can find its private sibling in ~/.ssh/", err)
		}
		logf("auto-detected signing key: %s\n", keyPath)
		return keyPath, args[0]
	}
	fail("usage: munisign sign [-key <keypath>] <dir>")
	return "", ""
}

// autodetectSigningKey returns the first private key under ~/.ssh/
// whose public half matches a fingerprint in keys/approved/. The
// match is strict: fingerprint equality, not filename heuristics.
func autodetectSigningKey() (string, error) {
	trust, err := munisign.LoadTrust()
	if err != nil {
		return "", fmt.Errorf("load trust: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sshDir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".pub") {
			continue
		}
		pubPath := filepath.Join(sshDir, name)
		data, err := os.ReadFile(pubPath)
		if err != nil {
			continue
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			continue
		}
		fp := ssh.FingerprintSHA256(k)
		if _, ok := trust.Approved[fp]; !ok {
			continue
		}
		privPath := strings.TrimSuffix(pubPath, ".pub")
		if _, err := os.Stat(privPath); err != nil {
			return "", fmt.Errorf("found approved pubkey %s but private sibling %s is missing", pubPath, privPath)
		}
		return privPath, nil
	}
	return "", fmt.Errorf("no private key in %s matches any fingerprint in keys/approved/", sshDir)
}

// promptUpload asks the user if they want to publish the signed bundle to
// DO Spaces. Skips if stdin isn't a terminal (scripted use).
func promptUpload(dir string) {
	fmt.Fprint(os.Stderr, "upload to data.thundercitizen.ca? [y/N] ")
	var resp string
	fmt.Fscanln(os.Stdin, &resp)
	resp = strings.ToLower(strings.TrimSpace(resp))
	if resp != "y" && resp != "yes" {
		logf("skipped upload — run `go run ./cmd/muni publish -dir %s` to upload later\n", dir)
		return
	}
	cmd := exec.Command("go", "run", "./cmd/muni", "publish", "-dir", dir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fail("publish failed: %v", err)
	}
}

func runVerify(args []string) {
	// Two modes:
	//   verify -key <pubkey> <dir>   — legacy single-key verify
	//   verify -trust <dir>          — use the embedded keys/ trust store
	if len(args) >= 2 && args[0] == "-trust" {
		dir := args[1]
		logf("verifying %s against embedded keys/approved (keys/revoked enforced)\n", dir)
		trust, err := munisign.LoadTrust()
		if err != nil {
			fail("load trust: %v", err)
		}
		v, err := munisign.VerifyFSWithTrust(os.DirFS(dir), trust)
		if err != nil {
			logf("FAIL: %v\n", err)
			os.Exit(1)
		}
		tk := trust.Approved[v.SignerFingerprint]
		logf("OK: merkle root %s\n", v.MerkleRoot)
		logf("signer: %s %s (keys/approved/%s)\n", v.SignerKey, v.SignerFingerprint, tk.Filename)
		return
	}

	keyPath, dir := parseKeyDir(args, "verify")

	logf("verifying %s against %s\n", dir, keyPath)

	pubKey, err := os.ReadFile(keyPath)
	if err != nil {
		fail("read key: %v", err)
	}

	v, err := munisign.VerifyFS(os.DirFS(dir), pubKey)
	if err != nil {
		logf("FAIL: %v\n", err)
		os.Exit(1)
	}

	logf("OK: merkle root %s\n", v.MerkleRoot)
	logf("signer: %s %s\n", v.SignerKey, v.SignerFingerprint)
}

// parseKeyDir extracts -key <path> and trailing <dir> from args.
func parseKeyDir(args []string, cmd string) (string, string) {
	if len(args) < 3 || args[0] != "-key" {
		fail("usage: munisign %s -key <keypath> <dir>", cmd)
	}
	return args[1], args[2]
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: munisign <command> [args]

commands:
  hash  <dir>                      print Merkle root hash
  sign  [-key <privkey>] <dir>     sign and write manifest.sig
                                   (omit -key to auto-detect from ~/.ssh
                                    against keys/approved/)
  verify -key <pubkey> <dir>       verify against a single explicit pubkey
  verify -trust <dir>              verify against the embedded keys/ trust store`)
	os.Exit(2)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
