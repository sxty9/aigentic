// Package chatstore persists a user's chat history server-side, keyed by the server-stamped
// holistic Subject, so chats follow the account across devices. The frontend's chat list is
// stored as one OPAQUE JSON blob per user at <usersDir>/<subject>/chats.json (0600): the backend
// never parses the chat structure — it only checks the blob is a JSON array within a size cap.
// Layout + the SafeSubject path-traversal guard mirror the per-user secret store.
package chatstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
)

// MaxBytes caps a user's stored chat history (defense against a runaway client).
const MaxBytes = 8 << 20 // 8 MiB

// ErrTooLarge / ErrBadJSON map to 413 / 400 at the HTTP edge.
var (
	ErrTooLarge = errors.New("chat history too large")
	ErrBadJSON  = errors.New("chat data is not a JSON array")
)

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

// Load returns the user's stored chat blob, or "[]" when none exists yet.
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

// Save validates that data is a JSON array within the size cap, then persists it atomically (0600).
func (s *Store) Save(subject string, data []byte) error {
	if len(data) > MaxBytes {
		return ErrTooLarge
	}
	var probe []json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return ErrBadJSON
	}
	p, err := s.path(subject)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, data)
}

// writeFileAtomic creates the parent dir (0700) and publishes content (0600) via a uniquely
// named temp file renamed over the target.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
