package chatstore

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// No file yet → an empty JSON array, not an error.
	b, err := s.Load("alice")
	if err != nil || string(b) != "[]" {
		t.Fatalf("default load = %q, %v; want \"[]\", nil", b, err)
	}

	blob := []byte(`[{"id":"1","title":"hi","messages":[],"updatedAt":1}]`)
	if err := s.Save("alice", blob); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load("alice")
	if err != nil || string(got) != string(blob) {
		t.Fatalf("roundtrip = %q, %v; want %q", got, err, blob)
	}

	// Stored 0600 under <dir>/<subject>/chats.json.
	info, err := os.Stat(filepath.Join(dir, "alice", "chats.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}

	// Isolation: bob sees his own empty store, not alice's.
	if b, _ := s.Load("bob"); string(b) != "[]" {
		t.Fatalf("bob load = %q; want \"[]\"", b)
	}
}

// TestPassiveRoundTripOpaqueBytes proves the pool stores and returns bytes VERBATIM and never
// interprets them: even non-JSON, an object (not an array), or raw binary round-trips unchanged
// (Passive Speicher). Evaluating the blob's shape is the caller's job — see the /chats handler —
// not the pool's. This is the inverse of the old in-pool validation.
func TestPassiveRoundTripOpaqueBytes(t *testing.T) {
	s := New(t.TempDir())
	for _, blob := range [][]byte{
		[]byte(`[{"role":"user","text":"hi"}]`), // a normal chat array
		[]byte("not json at all"),               // not JSON
		[]byte(`{"not":"an array"}`),             // JSON, but an object
		{0x00, 0x01, 0x02, 0xff},                 // raw binary
		{},                                       // empty
	} {
		if err := s.Save("bob", blob); err != nil {
			t.Fatalf("save %q: %v", blob, err)
		}
		got, err := s.Load("bob")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("round-trip mismatch: got %q want %q", got, blob)
		}
	}
}

// TestSaveAtomicUnderConcurrency proves concurrent saves for one subject leave a whole, single
// writer's blob — never a torn file — inheriting Atomare Zugriffe from the shared atomic primitive.
func TestSaveAtomicUnderConcurrency(t *testing.T) {
	s := New(t.TempDir())
	const n = 12
	whole := map[string]bool{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		blob := bytes.Repeat([]byte{byte('a' + i)}, i+1) // distinct content and length
		whole[string(blob)] = true
		wg.Add(1)
		go func(b []byte) {
			defer wg.Done()
			<-start
			if err := s.Save("carol", b); err != nil {
				t.Errorf("save: %v", err)
			}
		}(blob)
	}
	close(start)
	wg.Wait()

	got, err := s.Load("carol")
	if err != nil {
		t.Fatal(err)
	}
	if !whole[string(got)] {
		t.Fatalf("final blob is not any single writer's value (torn): %q", got)
	}
}

func TestBadSubjectRejected(t *testing.T) {
	s := New(t.TempDir())
	for _, bad := range []string{"../escape", "a/b", ".", ""} {
		if _, err := s.Load(bad); err == nil {
			t.Fatalf("Load(%q) = nil err; want rejection", bad)
		}
		if err := s.Save(bad, []byte("[]")); err == nil {
			t.Fatalf("Save(%q) = nil err; want rejection", bad)
		}
	}
}
