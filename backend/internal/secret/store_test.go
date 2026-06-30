package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const goodKey = "sk-ant-test-0123456789abcdef" // valid shape: sk-ant- prefix, >= 20 chars

func TestSetGetStatusClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anthropic.key")
	s := New(path, "")

	if _, ok := s.Get(); ok {
		t.Fatal("fresh store must be unconfigured")
	}
	if st := s.Status(); st.Configured {
		t.Fatalf("status configured before Set: %+v", st)
	}

	if err := s.Set(goodKey); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := s.Get()
	if !ok || got != goodKey {
		t.Fatalf("Get = %q ok=%v, want %q true", got, ok, goodKey)
	}

	// Persisted with restrictive permissions, and the value is the key.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}

	st := s.Status()
	if !st.Configured || st.Source != "store" {
		t.Errorf("status = %+v, want configured store", st)
	}
	if strings.Contains(st.Hint, goodKey) || !strings.HasSuffix(st.Hint, goodKey[len(goodKey)-4:]) {
		t.Errorf("hint %q must mask the key but end in its last 4 chars", st.Hint)
	}

	// A second store over the same path loads the persisted key.
	if got, ok := New(path, "").Get(); !ok || got != goodKey {
		t.Errorf("reload Get = %q ok=%v, want persisted key", got, ok)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := s.Get(); ok {
		t.Error("Get after Clear must be unconfigured")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("key file still present after Clear: %v", err)
	}
}

// The env key bootstraps the store; an admin Set overrides it (source flips to "store"); a
// Clear falls back to the env key again.
func TestEnvBootstrapOverrideFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anthropic.key")
	envKey := "sk-ant-env-0123456789abcdef"
	s := New(path, envKey)

	if got, ok := s.Get(); !ok || got != envKey {
		t.Fatalf("env bootstrap Get = %q ok=%v, want env key", got, ok)
	}
	if st := s.Status(); st.Source != "env" {
		t.Errorf("source = %q, want env", st.Source)
	}

	if err := s.Set(goodKey); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := s.Get(); got != goodKey {
		t.Errorf("after Set Get = %q, want stored key", got)
	}
	if st := s.Status(); st.Source != "store" {
		t.Errorf("source after Set = %q, want store", st.Source)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got, ok := s.Get(); !ok || got != envKey {
		t.Errorf("after Clear Get = %q ok=%v, want env fallback", got, ok)
	}
}

// A short/placeholder env key must never be echoed whole by Status — mask fully.
func TestStatusMasksShortEnvKey(t *testing.T) {
	s := New("", "test") // env-only bootstrap, value too short to reveal a tail
	st := s.Status()
	if !st.Configured || st.Source != "env" {
		t.Fatalf("status = %+v, want configured env", st)
	}
	if st.Hint != "…" || strings.Contains(st.Hint, "test") {
		t.Fatalf("hint %q must not reveal a short key", st.Hint)
	}
}

// Set and Clear must be safe under concurrent admin requests (run with -race): the on-disk
// key and the in-memory key are updated atomically together, so they never diverge.
func TestConcurrentSetClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anthropic.key")
	s := New(path, "")
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Set(goodKey) }()
		go func() { defer wg.Done(); _ = s.Clear() }()
	}
	wg.Wait()

	got, ok := s.Get()
	b, err := os.ReadFile(path)
	switch {
	case ok && err == nil:
		if strings.TrimSpace(string(b)) != got {
			t.Fatalf("disk/memory diverged: file=%q mem=%q", strings.TrimSpace(string(b)), got)
		}
	case ok && os.IsNotExist(err):
		t.Fatalf("memory holds key %q but file is absent", got)
	case !ok && err == nil:
		t.Fatalf("memory cleared but file still present: %q", strings.TrimSpace(string(b)))
	}
}

func TestSetRejectsBadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anthropic.key")
	s := New(path, "")
	for _, bad := range []string{"", "garbage", "sk-ant-short", "Bearer sk-ant-xxxxxxxxxxxxx"} {
		if err := s.Set(bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Set(%q) err = %v, want ErrInvalidKey", bad, err)
		}
	}
	if _, ok := s.Get(); ok {
		t.Error("a rejected Set must not configure the store")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected Set must not write the file")
	}
}
