package muni

import (
	"sync"
	"time"
)

// State is the coarse lifecycle of the async apply goroutine. Rendered
// directly onto the /data admin page so the operator can see what the
// background worker is up to.
type State string

const (
	StateIdle      State = "idle"
	StateFetching  State = "fetching"
	StateVerifying State = "verifying"
	StateApplying  State = "applying"
	StateOK        State = "ok"
	StateError     State = "error"
	StateSkipped   State = "skipped" // fast-path hit
)

// Status tracks what the apply goroutine is doing, plus the last
// success and failure. All fields are guarded by the embedded mutex
// so HTTP handlers can read it concurrently.
type Status struct {
	mu            sync.RWMutex
	state         State
	lastError     string
	lastErrorAt   time.Time
	lastSuccessAt time.Time
	signerFP      string
	signerFile    string
	merkleRoot    string
	datasets      int
	message       string
}

// NewStatus returns a Status in the idle state.
func NewStatus() *Status {
	return &Status{state: StateIdle}
}

// SetState updates the current state and optional message. Zero-value
// messages are ignored so callers can set state without clobbering the
// last message.
func (s *Status) SetState(state State, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	if message != "" {
		s.message = message
	}
}

// SetError records a terminal failure for this apply cycle.
func (s *Status) SetError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateError
	s.lastError = err.Error()
	s.lastErrorAt = time.Now()
}

// SetSuccess records a completed apply cycle with the bundle metadata.
// Clears any prior error and the in-flight status message so the admin
// page shows green again without stale "applying bundle" crumbs.
func (s *Status) SetSuccess(signerFP, signerFile, merkle string, datasets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateOK
	s.signerFP = signerFP
	s.signerFile = signerFile
	s.merkleRoot = merkle
	s.datasets = datasets
	s.lastSuccessAt = time.Now()
	s.lastError = ""
	s.lastErrorAt = time.Time{}
	s.message = ""
}

// SetSkipped records a dev fast-path hit.
func (s *Status) SetSkipped(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateSkipped
	s.message = message
	s.lastSuccessAt = time.Now()
}

// Snapshot returns a read-only copy suitable for template rendering.
func (s *Status) Snapshot() StatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StatusSnapshot{
		State:         s.state,
		Message:       s.message,
		LastError:     s.lastError,
		LastErrorAt:   s.lastErrorAt,
		LastSuccessAt: s.lastSuccessAt,
		SignerFP:      s.signerFP,
		SignerFile:    s.signerFile,
		MerkleRoot:    s.merkleRoot,
		Datasets:      s.datasets,
	}
}

// StatusSnapshot is an immutable view of Status for display.
type StatusSnapshot struct {
	State         State
	Message       string
	LastError     string
	LastErrorAt   time.Time
	LastSuccessAt time.Time
	SignerFP      string
	SignerFile    string
	MerkleRoot    string
	Datasets      int
}
