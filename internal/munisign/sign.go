package munisign

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SignFS computes the Merkle root of all files in dir (excluding
// manifest.sig), then signs it by shelling out to ssh-keygen.
//
// For FIDO2 keys this requires user presence (physical touch).
// logf receives progress messages (pass fmt.Printf or nil to suppress).
// Returns the SSHSIG armored bytes, ready to write as manifest.sig.
func SignFS(dir string, keyPath string, logf func(string, ...any)) ([]byte, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Check key file exists before doing anything.
	if _, err := os.Stat(keyPath); err != nil {
		return nil, fmt.Errorf("munisign: key not found: %s", keyPath)
	}

	logf("hashing %s\n", dir)
	fsys := os.DirFS(dir)
	entries, _ := fs.ReadDir(fsys, ".")
	count := 0
	for _, e := range entries {
		if !e.IsDir() && e.Name() != ManifestFile {
			count++
		}
	}

	rootHash, err := HashFS(fsys, map[string]bool{ManifestFile: true})
	if err != nil {
		return nil, fmt.Errorf("munisign: hashing %s: %w", dir, err)
	}
	logf("merkle root: %s (%d files)\n", rootHash, count)

	isSK := isSKKey(keyPath)
	if isSK {
		logf("touch your security key to sign\n")
	} else {
		logf("signing with %s\n", keyPath)
	}

	sig, err := signWithSSHKeygen(rootHash, keyPath)
	if err != nil {
		if isSK {
			return nil, fmt.Errorf("munisign: signing failed — is your security key plugged in?")
		}
		return nil, err
	}

	logf("signed\n")
	return sig, nil
}

// isSKKey checks if the public key file contains an SK key type (FIDO2).
func isSKKey(keyPath string) bool {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "sk-ecdsa-sha2-") || strings.Contains(s, "sk-ssh-ed25519")
}

// signWithSSHKeygen pipes the message to ssh-keygen -Y sign and returns
// the armored SSHSIG output. Times out after 30 seconds (covers the case
// where FIDO2 key is not plugged in and ssh-keygen hangs).
func signWithSSHKeygen(message string, keyPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"ssh-keygen",
		"-Y", "sign",
		"-f", keyPath,
		"-n", Namespace,
	)
	cmd.Stdin = strings.NewReader(message)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("munisign: ssh-keygen timed out after 30s — is your security key plugged in?")
		}
		return nil, fmt.Errorf("munisign: ssh-keygen failed: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	return stdout.Bytes(), nil
}
