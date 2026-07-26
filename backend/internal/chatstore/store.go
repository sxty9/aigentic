// Package chatstore persists a user's chat history server-side, keyed by the server-stamped
// holistic Subject, so chats follow the account across devices.
//
// It is a PASSIVE pool (Holistic "Passive Speicher" axiom): it holds one OPAQUE byte blob per user
// at <usersDir>/<subject>/chats.json (0600) and never interprets it. The blob's shape (a JSON array)
// and size are evaluated OUTSIDE the pool — by the HTTP handler that owns the /chats endpoint —
// exactly as the graveyard store keeps its policy in the calling service. Reads and writes are
// atomic (Atomare Zugriffe) via the shared atomicfile primitive. Layout + the SafeSubject
// path-traversal guard mirror the per-user secret store.
package chatstore

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sxty9/aigentic/backend/internal/atomicfile"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
)

// MaxBytes caps a user's stored chat history (defense against a runaway client). The cap is
// EVALUATED by the caller (the HTTP edge), not here: a passive pool does not evaluate the data it
// holds. It is exported so the single evaluation point references one canonical constant.
const MaxBytes = 8 << 20 // 8 MiB

var empty = []byte("[]")

// Store reads/writes per-user chat blobs under usersDir (the same root as the secret store's
// per-user credentials).
type Store struct {
	usersDir string
}

func New(usersDir string) *Store { return &Store{usersDir: usersDir} }

func (s *Store) path(subject string) (string, error) {
	safe, err := secretstore.SafeSubject(subject)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.usersDir, safe, "chats.json"), nil
}

// Load returns the user's stored chat blob VERBATIM, or "[]" when none exists yet. It never parses
// the blob (Passive Speicher) and the read is atomic (one whole file or the empty default).
func (s *Store) Load(subject string) ([]byte, error) {
	p, err := s.path(subject)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return append([]byte(nil), empty...), nil
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Save persists data as one opaque blob atomically (0600). It does NOT inspect the bytes: shape and
// size are the caller's to validate (Passive Speicher — every evaluation happens outside the pool).
func (s *Store) Save(subject string, data []byte) error {
	p, err := s.path(subject)
	if err != nil {
		return err
	}
	return atomicfile.Write(p, data, 0o600)
}
