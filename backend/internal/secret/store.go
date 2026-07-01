// Package secret persists Anthropic/Claude credentials for the aigentic daemon.
//
// It holds three tiers, all under the systemd StateDirectory (/var/lib/aigentic), 0600:
//   - a GLOBAL admin key (optional shared fallback), backed by `path` (anthropic.key);
//   - PER-USER credentials under `usersDir`/<subject>/: api.key (the user's own Anthropic API
//     key) and claude-oauth.token (a `claude setup-token`), plus a per-user claude/ directory
//     used as the CLI's CLAUDE_CONFIG_DIR.
//
// Credentials are NEVER returned over the wire — only masked status (configured/source/hint).
// Per-user isolation is by directory (all owned by the single unprivileged service user); the
// Subject is the server-stamped holistic username, sanitised by SafeSubject before it becomes
// a path. ANTHROPIC_API_KEY remains a read-only bootstrap for the global tier.
package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	keyPrefix          = "sk-ant-"    // shared Anthropic prefix (API keys AND OAuth tokens)
	apiKeyPrefix       = "sk-ant-api" // Anthropic API keys
	oauthPrefix        = "sk-ant-oat" // `claude setup-token` subscription OAuth tokens
	apiKeyFile         = "api.key"
	oauthTokenFile     = "claude-oauth.token"
	claudeConfigSubdir = "claude"
)

var (
	// ErrInvalidKey means the supplied value is not a plausible Anthropic API key.
	ErrInvalidKey = errors.New("not a valid Anthropic API key (want sk-ant-…, at least 20 chars)")
	// ErrInvalidToken means the supplied value is not a plausible Claude setup-token.
	ErrInvalidToken = errors.New("not a valid Claude setup-token")
)

// Store persists the global admin key in memory (backed by a file) and per-user credentials
// on disk (read on demand). The daemon is the only writer.
type Store struct {
	path     string // global admin key file (optional shared fallback); "" => no global persistence
	usersDir string // per-user credential root (e.g. /var/lib/aigentic/users); "" => no per-user storage
	env      string // ANTHROPIC_API_KEY bootstrap (read-only, feeds the global tier)

	mu     sync.RWMutex
	gkey   string // global admin key (file, else env)
	gstore bool   // true => global key came from the file (admin-set), false => env/none
}

// New builds a store. path is the global admin-key file; usersDir is the per-user credential
// root; envKey is the ANTHROPIC_API_KEY bootstrap for the global tier.
func New(path, usersDir, envKey string) *Store {
	s := &Store{path: path, usersDir: usersDir, env: strings.TrimSpace(envKey)}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				s.gkey, s.gstore = k, true
			}
		}
	}
	if !s.gstore {
		s.gkey = s.env
	}
	return s
}

// Status reports configured/source/hint WITHOUT revealing the key.
type Status struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"` // "user" | "store" (admin) | "env"
	Hint       string `json:"hint,omitempty"`   // masked: sk-ant-…last4
}

// TokenStatus reports whether a user has linked a Claude subscription token (masked).
type TokenStatus struct {
	Linked bool   `json:"linked"`
	Hint   string `json:"hint,omitempty"` // masked: sk-ant-oat…last4
}

// Key returns the API key to bill a request from `subject`: the user's own key if set, else
// the global admin key, else the env bootstrap. Wired into ClaudeAPIConfig.KeyFunc, read per
// request so a change takes effect without a restart.
func (s *Store) Key(subject string) (string, bool) {
	if s == nil {
		return "", false
	}
	if k, ok := s.readUserFile(subject, apiKeyFile); ok && k != "" {
		return k, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gkey, s.gkey != ""
}

// UserKeyStatus reports the EFFECTIVE api-key status for a user (own → global → env), so the
// per-user panel can show "your key", "using the shared key", or "not configured".
func (s *Store) UserKeyStatus(subject string) Status {
	if k, ok := s.readUserFile(subject, apiKeyFile); ok && k != "" {
		return Status{Configured: true, Source: "user", Hint: mask(k)}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.gkey != "" {
		src := "env"
		if s.gstore {
			src = "store"
		}
		return Status{Configured: true, Source: src, Hint: mask(s.gkey)}
	}
	return Status{}
}

// SetUserKey validates and persists a user's own Anthropic API key.
func (s *Store) SetUserKey(subject, key string) error {
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return ErrInvalidKey
	}
	return s.writeUserFile(subject, apiKeyFile, key)
}

// ClearUserKey removes a user's own API key (they fall back to the global/env tier).
func (s *Store) ClearUserKey(subject string) error { return s.removeUserFile(subject, apiKeyFile) }

// --- global admin key (backs the admin /secret endpoints) ---

// GlobalStatus reports the global admin key's status (the optional shared fallback).
func (s *Store) GlobalStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Configured: s.gkey != ""}
	if !st.Configured {
		return st
	}
	if s.gstore {
		st.Source = "store"
	} else {
		st.Source = "env"
	}
	st.Hint = mask(s.gkey)
	return st
}

// SetGlobal validates and persists the global admin key (atomic, 0600).
func (s *Store) SetGlobal(key string) error {
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return ErrInvalidKey
	}
	if s.path == "" {
		return errors.New("secret store has no backing path (set AIGENTIC_SECRET_FILE)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path, key); err != nil {
		return err
	}
	s.gkey, s.gstore = key, true
	return nil
}

// ClearGlobal removes the global admin key file and falls back to the env bootstrap.
func (s *Store) ClearGlobal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.gstore, s.gkey = false, s.env
	return nil
}

// --- per-user Claude subscription token (from `claude setup-token`) ---

// OAuthToken returns a user's linked Claude subscription token, if any. Wired into the
// claude-cli leaf, which injects it as CLAUDE_CODE_OAUTH_TOKEN.
func (s *Store) OAuthToken(subject string) (string, bool) {
	if s == nil {
		return "", false
	}
	t, ok := s.readUserFile(subject, oauthTokenFile)
	return t, ok && t != ""
}

// TokenStatus reports whether a user has linked a subscription token (masked, never the value).
func (s *Store) TokenStatus(subject string) TokenStatus {
	if t, ok := s.readUserFile(subject, oauthTokenFile); ok && t != "" {
		return TokenStatus{Linked: true, Hint: mask(t)}
	}
	return TokenStatus{}
}

// LinkToken validates and persists a user's Claude setup-token.
func (s *Store) LinkToken(subject, token string) error {
	token = strings.TrimSpace(token)
	if !validToken(token) {
		return ErrInvalidToken
	}
	return s.writeUserFile(subject, oauthTokenFile, token)
}

// UnlinkToken removes a user's token AND their CLI config/session dir, so stale credentials
// don't linger after they unlink.
func (s *Store) UnlinkToken(subject string) error {
	if err := s.removeUserFile(subject, oauthTokenFile); err != nil {
		return err
	}
	if dir, err := s.userPath(subject, claudeConfigSubdir); err == nil {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// ConfigDir returns (creating) the per-user CLAUDE_CONFIG_DIR for the claude CLI, so each
// user's session/credentials live in their own directory rather than the service account's.
func (s *Store) ConfigDir(subject string) (string, error) {
	dir, err := s.userPath(subject, claudeConfigSubdir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// --- per-user file helpers ---

// userPath joins usersDir/<safe-subject>/elems… and asserts the result stays under the user's
// own directory (defense in depth on top of SafeSubject).
func (s *Store) userPath(subject string, elems ...string) (string, error) {
	if s == nil || s.usersDir == "" {
		return "", errors.New("no per-user storage configured")
	}
	safe, err := SafeSubject(subject)
	if err != nil {
		return "", err
	}
	base := filepath.Join(s.usersDir, safe)
	p := filepath.Join(append([]string{base}, elems...)...)
	if p != base && !strings.HasPrefix(p, base+string(os.PathSeparator)) {
		return "", ErrBadSubject
	}
	return p, nil
}

func (s *Store) readUserFile(subject, name string) (string, bool) {
	p, err := s.userPath(subject, name)
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func (s *Store) writeUserFile(subject, name, content string) error {
	p, err := s.userPath(subject, name)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, content)
}

func (s *Store) removeUserFile(subject, name string) error {
	p, err := s.userPath(subject, name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeFileAtomic creates the parent dir (0700) and publishes content (0600) via a uniquely
// named temp file renamed over the target.
func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content + "\n"); err != nil {
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

// --- validation + masking ---

// validKey accepts an Anthropic API key (sk-ant-api…), but NOT a subscription OAuth token
// (sk-ant-oat…), which shares the sk-ant- prefix but belongs in the claude-oauth slot.
func validKey(k string) bool {
	return strings.HasPrefix(k, keyPrefix) && !strings.HasPrefix(k, oauthPrefix) && len(k) >= 20
}

// validToken accepts a Claude setup-token: a single opaque string, no whitespace, reasonably
// long. `claude setup-token` emits an OAuth token that starts with sk-ant-oat… — i.e. it SHARES
// the sk-ant- prefix with API keys, so we must NOT reject on that. We only reject a plain API key
// (sk-ant-api…) pasted into the wrong slot; any other plausible token is accepted.
func validToken(t string) bool {
	if len(t) < 20 || strings.ContainsAny(t, " \t\r\n") {
		return false
	}
	return !strings.HasPrefix(t, apiKeyPrefix)
}

// mask reveals only a recognizable prefix and the last 4 chars; a value too short to reveal a
// tail safely is masked entirely. mask NEVER returns the whole input.
func mask(k string) string {
	if len(k) < 12 {
		return "…"
	}
	last := k[len(k)-4:]
	switch {
	case strings.HasPrefix(k, oauthPrefix):
		return oauthPrefix + "…" + last
	case strings.HasPrefix(k, keyPrefix):
		return keyPrefix + "…" + last
	default:
		return "…" + last
	}
}
