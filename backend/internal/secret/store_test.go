package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	goodKey   = "sk-ant-test-0123456789abcdef"            // valid api-key shape
	goodKey2  = "sk-ant-user-abcdef0123456789"            // a second, distinct api key
	goodToken = "claude_token_0123456789abcdef0123456789" // valid setup-token shape
)

func newStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "anthropic.key")
	usersDir := filepath.Join(dir, "users")
	return New(globalPath, usersDir, ""), globalPath, usersDir
}

// A user's own key wins; without one they fall back to the global admin key, then env.
func TestKeyPrecedence(t *testing.T) {
	s, _, _ := newStore(t)

	// Nothing set anywhere.
	if _, ok := s.Key("alice"); ok {
		t.Fatal("no key configured, want not-ok")
	}

	// Global admin key → every user falls back to it.
	if err := s.SetGlobal(goodKey); err != nil {
		t.Fatalf("SetGlobal: %v", err)
	}
	if k, ok := s.Key("alice"); !ok || k != goodKey {
		t.Fatalf("alice Key = %q ok=%v, want global %q", k, ok, goodKey)
	}
	if st := s.UserKeyStatus("alice"); st.Source != "store" {
		t.Errorf("alice source = %q, want store (shared fallback)", st.Source)
	}

	// Alice sets her own → overrides the global; bob still uses global.
	if err := s.SetUserKey("alice", goodKey2); err != nil {
		t.Fatalf("SetUserKey: %v", err)
	}
	if k, _ := s.Key("alice"); k != goodKey2 {
		t.Errorf("alice Key = %q, want her own %q", k, goodKey2)
	}
	if st := s.UserKeyStatus("alice"); st.Source != "user" {
		t.Errorf("alice source = %q, want user", st.Source)
	}
	if k, _ := s.Key("bob"); k != goodKey {
		t.Errorf("bob Key = %q, want global %q", k, goodKey)
	}

	// Per-user file is 0600 and not the global file.
	p := filepath.Join(s.usersDir, "alice", "api.key")
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("alice key file mode: %v err=%v, want 0600", info, err)
	}

	// Clearing alice's own key falls her back to the global.
	if err := s.ClearUserKey("alice"); err != nil {
		t.Fatalf("ClearUserKey: %v", err)
	}
	if k, _ := s.Key("alice"); k != goodKey {
		t.Errorf("after clear, alice Key = %q, want global", k)
	}
}

// Env bootstrap feeds the global tier when no file is set.
func TestEnvBootstrap(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "anthropic.key"), filepath.Join(dir, "users"), "sk-ant-env-0123456789abcdef")
	if k, ok := s.Key("anyone"); !ok || !strings.HasPrefix(k, "sk-ant-env") {
		t.Fatalf("env bootstrap Key = %q ok=%v", k, ok)
	}
	if st := s.UserKeyStatus("anyone"); st.Source != "env" {
		t.Errorf("source = %q, want env", st.Source)
	}
}

// Subscription token: link, read, status (masked), unlink.
func TestTokenLinkUnlink(t *testing.T) {
	s, _, _ := newStore(t)

	if st := s.TokenStatus("alice"); st.Linked {
		t.Fatal("token not linked yet")
	}
	if err := s.LinkToken("alice", goodToken); err != nil {
		t.Fatalf("LinkToken: %v", err)
	}
	if tok, ok := s.OAuthToken("alice"); !ok || tok != goodToken {
		t.Fatalf("OAuthToken = %q ok=%v, want %q", tok, ok, goodToken)
	}
	st := s.TokenStatus("alice")
	if !st.Linked || st.Hint == "" || strings.Contains(st.Hint, goodToken) {
		t.Fatalf("token status leaks or wrong: %+v", st)
	}

	// ConfigDir is created 0700 under the user's dir.
	cfg, err := s.ConfigDir("alice")
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if info, err := os.Stat(cfg); err != nil || !info.IsDir() {
		t.Fatalf("config dir not created: %v", err)
	}

	if err := s.UnlinkToken("alice"); err != nil {
		t.Fatalf("UnlinkToken: %v", err)
	}
	if _, ok := s.OAuthToken("alice"); ok {
		t.Error("token still present after unlink")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Error("config dir should be removed on unlink")
	}
}

// Validation: bad api keys and bad tokens are rejected and not persisted; an api key is not a
// valid token and vice-versa.
func TestValidation(t *testing.T) {
	s, _, _ := newStore(t)
	for _, bad := range []string{"", "garbage", "sk-ant-short", "Bearer x"} {
		if err := s.SetUserKey("alice", bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("SetUserKey(%q) = %v, want ErrInvalidKey", bad, err)
		}
	}
	for _, bad := range []string{"", "short", goodKey /* an api key is not a token */, "has space inside it xx"} {
		if err := s.LinkToken("alice", bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("LinkToken(%q) = %v, want ErrInvalidToken", bad, err)
		}
	}
	if _, ok := s.Key("alice"); ok {
		t.Error("rejected sets must not configure anything")
	}
}

// A Subject that isn't a safe path segment is rejected — never written to disk.
func TestSafeSubjectRejected(t *testing.T) {
	s, _, usersDir := newStore(t)
	for _, bad := range []string{"..", "../etc", "a/b", ".hidden", ""} {
		if err := s.SetUserKey(bad, goodKey); !errors.Is(err, ErrBadSubject) {
			t.Errorf("SetUserKey(subject=%q) = %v, want ErrBadSubject", bad, err)
		}
		if err := s.LinkToken(bad, goodToken); !errors.Is(err, ErrBadSubject) {
			t.Errorf("LinkToken(subject=%q) = %v, want ErrBadSubject", bad, err)
		}
	}
	// Nothing escaped the users dir.
	if entries, _ := os.ReadDir(usersDir); len(entries) != 0 {
		t.Errorf("bad subjects created %d dir entries, want 0", len(entries))
	}
}

// Mask never returns the whole secret.
func TestMaskNeverLeaks(t *testing.T) {
	if h := mask(goodKey); strings.Contains(h, goodKey) || !strings.HasSuffix(h, goodKey[len(goodKey)-4:]) {
		t.Errorf("api mask %q leaks or lacks last4", h)
	}
	if h := mask(goodToken); strings.Contains(h, goodToken) || !strings.HasPrefix(h, "claude_token_…") {
		t.Errorf("token mask %q wrong", h)
	}
	if mask("short") != "…" {
		t.Error("short value must be fully masked")
	}
}

// Concurrent per-user writes/reads are race-free (run with -race).
func TestConcurrentPerUser(t *testing.T) {
	s, _, _ := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = s.SetUserKey("alice", goodKey) }()
		go func() { defer wg.Done(); _ = s.LinkToken("alice", goodToken) }()
		go func() { defer wg.Done(); _, _ = s.Key("alice") }()
	}
	wg.Wait()
}
