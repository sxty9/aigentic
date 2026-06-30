// Package secret persists the operator-supplied Anthropic API key for the aigentic daemon.
// The key is admin-managed at runtime through the dashboard, so it must live in a file the
// unprivileged daemon can itself write — under the systemd StateDirectory (/var/lib/aigentic),
// mode 0600. It is NEVER returned over the wire: only a masked hint (sk-ant-…last4) and a
// configured/source flag are exposed. ANTHROPIC_API_KEY remains a read-only bootstrap that a
// stored key overrides and a clear falls back to.
package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// keyPrefix is the Anthropic API-key prefix; Set rejects anything else so an admin can't
// store a stray value that would only later 401 the paid engine.
const keyPrefix = "sk-ant-"

// ErrInvalidKey means the supplied value is not a plausible Anthropic API key.
var ErrInvalidKey = errors.New("not a valid Anthropic API key (want sk-ant-…, at least 20 chars)")

// Store holds the active API key in memory, backed by a file. The daemon is the only writer,
// so the in-memory copy is authoritative once loaded; Set/Clear keep file and memory in sync.
type Store struct {
	path string // file backing the stored key; "" => no persistence (env/Set-in-memory only)
	env  string // ANTHROPIC_API_KEY bootstrap fallback (read-only)

	mu     sync.RWMutex
	key    string // the active key (stored file wins, else env fallback)
	stored bool   // true => key came from the file (admin-set); false => env fallback/none
}

// New builds a store. path is the backing file (e.g. $STATE_DIRECTORY/anthropic.key); envKey
// is the ANTHROPIC_API_KEY bootstrap. Precedence: a stored file wins over env, so an admin can
// override the deployed key — or, by clearing, fall back to it.
func New(path, envKey string) *Store {
	s := &Store{path: path, env: strings.TrimSpace(envKey)}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				s.key, s.stored = k, true
			}
		}
	}
	if !s.stored {
		s.key = s.env
	}
	return s
}

// Get returns the active key and whether one is configured. Wired into
// ClaudeAPIConfig.KeyFunc so the paid leaf reads the current key on every request — an admin
// change takes effect without a restart.
func (s *Store) Get() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.key, s.key != ""
}

// Status reports configured/source/hint WITHOUT revealing the key (hint = sk-ant-…last4).
type Status struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"` // "store" (admin-set) | "env"
	Hint       string `json:"hint,omitempty"`   // masked: sk-ant-…last4
}

// Status returns the current, non-secret status of the stored key.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Configured: s.key != ""}
	if !st.Configured {
		return st
	}
	if s.stored {
		st.Source = "store"
	} else {
		st.Source = "env"
	}
	st.Hint = mask(s.key)
	return st
}

// Set validates and persists a new key (atomic write, mode 0600), then activates it. The
// lock is held across the whole publish so concurrent admin Set/Clear cannot interleave the
// file operation with the in-memory update (which would diverge disk from the live key).
func (s *Store) Set(key string) error {
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return ErrInvalidKey
	}
	if s.path == "" {
		return errors.New("secret store has no backing path (set AIGENTIC_SECRET_FILE)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Atomic publish via a uniquely-named temp file, then rename over the target. CreateTemp
	// makes the file 0600 on creation, so the mode never depends on a pre-existing temp, and
	// the random name means two concurrent Sets can't collide on a shared path.
	tmp, err := os.CreateTemp(dir, ".anthropic-*.key")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(key + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	s.key, s.stored = key, true
	return nil
}

// Clear removes the stored key file and falls back to the env bootstrap (if any). Holds the
// lock across the file removal + in-memory update, matching Set.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.stored, s.key = false, s.env
	return nil
}

func validKey(k string) bool { return strings.HasPrefix(k, keyPrefix) && len(k) >= 20 }

// mask reveals only the prefix (when present) and the last 4 chars: sk-ant-…AB12. A value
// too short to reveal a tail without disclosing most of itself (e.g. a stray/placeholder env
// key) is masked entirely — mask never returns the whole input.
func mask(k string) string {
	if len(k) < 12 {
		return "…"
	}
	prefix := ""
	if strings.HasPrefix(k, keyPrefix) {
		prefix = keyPrefix
	}
	return prefix + "…" + k[len(k)-4:]
}
