package munisign

import (
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

const sshsigMagic = "SSHSIG"
const sshsigVersion = 1
const sshsigPEMType = "SSH SIGNATURE"

// sshSig is a parsed SSHSIG structure as defined in OpenSSH PROTOCOL.sshsig.
type sshSig struct {
	Version   uint32
	PublicKey ssh.PublicKey
	Namespace string
	HashAlg   string // "sha256" or "sha512"
	Signature *ssh.Signature
}

// sshSigWire maps to the binary layout for ssh.Unmarshal.
type sshSigWire struct {
	Magic     [6]byte `ssh:"fixed"`
	Version   uint32
	PublicKey []byte `ssh:"rest_is_not_used"` // parsed manually below
}

// parseSSHSig decodes a PEM-armored SSHSIG blob (as produced by
// ssh-keygen -Y sign) and returns the parsed structure.
func parseSSHSig(data []byte) (*sshSig, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("munisign: no PEM block found")
	}
	if block.Type != sshsigPEMType {
		return nil, fmt.Errorf("munisign: PEM type %q, want %q", block.Type, sshsigPEMType)
	}

	return parseSSHSigBinary(block.Bytes)
}

// parseSSHSigBinary parses the raw binary SSHSIG payload.
//
// Wire format (from PROTOCOL.sshsig):
//
//	byte[6]   "SSHSIG"
//	uint32    version (1)
//	string    publickey (SSH wire format)
//	string    namespace
//	string    reserved (empty)
//	string    hash_algorithm
//	string    signature (SSH signature blob)
func parseSSHSigBinary(data []byte) (*sshSig, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("munisign: sshsig too short")
	}
	if string(data[:6]) != sshsigMagic {
		return nil, fmt.Errorf("munisign: bad magic %q", data[:6])
	}
	rest := data[6:]

	// version (uint32, big-endian)
	if len(rest) < 4 {
		return nil, fmt.Errorf("munisign: truncated version")
	}
	version := binary.BigEndian.Uint32(rest[:4])
	if version != sshsigVersion {
		return nil, fmt.Errorf("munisign: unsupported version %d", version)
	}
	rest = rest[4:]

	// publickey (string = uint32 length + bytes)
	pubKeyBytes, rest, err := parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("munisign: publickey: %w", err)
	}
	pubKey, err := ssh.ParsePublicKey(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("munisign: parse publickey: %w", err)
	}

	// namespace
	nsBytes, rest, err := parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("munisign: namespace: %w", err)
	}

	// reserved (ignored)
	_, rest, err = parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("munisign: reserved: %w", err)
	}

	// hash_algorithm
	algBytes, rest, err := parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("munisign: hash_algorithm: %w", err)
	}

	// signature blob (SSH wire format: string type + string blob)
	sigBytes, _, err := parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("munisign: signature: %w", err)
	}
	sig, err := parseSSHSignature(sigBytes)
	if err != nil {
		return nil, fmt.Errorf("munisign: parse signature: %w", err)
	}

	return &sshSig{
		Version:   version,
		PublicKey: pubKey,
		Namespace: string(nsBytes),
		HashAlg:   string(algBytes),
		Signature: sig,
	}, nil
}

// signedData reconstructs the blob that ssh-keygen passes to the SSH
// signing function, per PROTOCOL.sshsig:
//
//	byte[6]   "SSHSIG"
//	string    namespace
//	string    reserved (empty)
//	string    hash_algorithm
//	string    H(message)
//
// The message is our Merkle root hex string. H() is SHA-512 (the default
// hash algorithm ssh-keygen uses for SSHSIG).
func signedData(namespace, hashAlg string, message []byte) []byte {
	var h []byte
	switch hashAlg {
	case "sha512":
		sum := sha512.Sum512(message)
		h = sum[:]
	case "sha256":
		// Unlikely for SSHSIG but handle for completeness.
		sum := sha512.Sum512(message) // SSHSIG always uses sha512 for H(message)
		h = sum[:]
	default:
		sum := sha512.Sum512(message)
		h = sum[:]
	}

	var buf []byte
	buf = append(buf, []byte(sshsigMagic)...)
	buf = appendSSHString(buf, []byte(namespace))
	buf = appendSSHString(buf, nil) // reserved
	buf = appendSSHString(buf, []byte(hashAlg))
	buf = appendSSHString(buf, h)
	return buf
}

// parseSSHString reads a uint32-length-prefixed byte string.
func parseSSHString(data []byte) ([]byte, []byte, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("truncated string length")
	}
	n := binary.BigEndian.Uint32(data[:4])
	data = data[4:]
	if uint32(len(data)) < n {
		return nil, nil, fmt.Errorf("string length %d exceeds remaining %d bytes", n, len(data))
	}
	return data[:n], data[n:], nil
}

// appendSSHString appends a uint32-length-prefixed byte string.
func appendSSHString(buf, s []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, s...)
	return buf
}

// parseSSHSignature parses an SSH signature blob (format string + blob bytes).
func parseSSHSignature(data []byte) (*ssh.Signature, error) {
	formatBytes, rest, err := parseSSHString(data)
	if err != nil {
		return nil, fmt.Errorf("signature format: %w", err)
	}
	blobBytes, rest, err := parseSSHString(rest)
	if err != nil {
		return nil, fmt.Errorf("signature blob: %w", err)
	}
	return &ssh.Signature{
		Format: string(formatBytes),
		Blob:   blobBytes,
		Rest:   rest, // FIDO2 flags+counter for SK keys
	}, nil
}
