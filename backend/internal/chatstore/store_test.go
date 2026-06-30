package chatstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestSaveValidation(t *testing.T) {
	s := New(t.TempDir())

	if err := s.Save("alice", []byte(`{"not":"an array"}`)); !errors.Is(err, ErrBadJSON) {
		t.Fatalf("object body: err = %v; want ErrBadJSON", err)
	}
	if err := s.Save("alice", []byte(`not json at all`)); !errors.Is(err, ErrBadJSON) {
		t.Fatalf("garbage body: err = %v; want ErrBadJSON", err)
	}
	big := []byte("[" + strings.Repeat("0,", MaxBytes) + "0]")
	if err := s.Save("alice", big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize body: err = %v; want ErrTooLarge", err)
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
