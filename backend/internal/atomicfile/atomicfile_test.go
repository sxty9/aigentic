package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWriteCreatesParentAndPerms proves Write creates a missing parent directory and applies the
// exact permission bits (not the umask).
func TestWriteCreatesParentAndPerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "file")
	if err := Write(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read back = %q, %v", b, err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", fi.Mode().Perm())
	}
}

// TestWriteOverwritesWholeFile proves a second write fully replaces the file — no leftover bytes
// from a longer previous value survive.
func TestWriteOverwritesWholeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	if err := Write(p, []byte("a long first value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "short" {
		t.Fatalf("content = %q, want exactly \"short\"", b)
	}
}

// TestWriteAtomicSingleWinner hammers one path with many concurrent writers of DISTINCT contents.
// Because the publish is an atomic rename, the final file must equal exactly one writer's full
// content, and a concurrent reader must only ever observe a WHOLE candidate — never a torn or
// interleaved mix (Atomare Zugriffe: unteilbar, ohne beobachtbaren Zwischenzustand).
func TestWriteAtomicSingleWinner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "contended")

	const n = 16
	candidates := make([]string, n)
	whole := map[string]bool{}
	for i := 0; i < n; i++ {
		// Distinct content AND length, so a torn read cannot masquerade as a valid candidate.
		candidates[i] = fmt.Sprintf("writer-%02d:%s", i, strings.Repeat(string(rune('A'+i)), i*7+1))
		whole[candidates[i]] = true
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range candidates {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			<-start
			if err := Write(p, []byte(content), 0o600); err != nil {
				t.Errorf("write: %v", err)
			}
		}(c)
	}

	// Reader: while writers race, every successful read must be one complete candidate.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue // absent before the first rename is fine
			}
			if !whole[string(b)] {
				t.Errorf("torn read observed: %q is not a whole candidate", b)
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	close(stop)
	rwg.Wait()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !whole[string(b)] {
		t.Fatalf("final content is not any single writer's value: %q", b)
	}
}

// TestWriteLeavesNoTemp proves a successful write renames the temp away (no stray .tmp-* remains).
func TestWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("stray temp left behind: %s", e.Name())
		}
	}
}

// TestWriteEmptyContent proves a zero-length write is valid (the empty blob is a legitimate value).
func TestWriteEmptyContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty")
	if err := Write(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(b, []byte{}) {
		t.Fatalf("empty write round-trip = %q, %v", b, err)
	}
}
