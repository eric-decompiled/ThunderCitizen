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
	keyPath, dir := parseKeyDir(args, "sign")

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
  hash  <dir>                 print Merkle root hash
  sign  -key <privkey> <dir>  sign and write manifest.sig
  verify -key <pubkey> <dir>  verify manifest.sig`)
	os.Exit(2)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
