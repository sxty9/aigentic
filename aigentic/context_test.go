package aigentic

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func envFor(grave graveyard.Graveyard, subject string) prizm.Env {
	return prizm.Env{Grave: grave, Header: prizm.Header{Subject: subject}}
}

// itemFor returns the ContextItem for a path suffix (rel paths are scope-relative).
func itemFor(items []ContextItem, suffix string) *ContextItem {
	for i := range items {
		if strings.HasSuffix(items[i].Path, suffix) {
			return &items[i]
		}
	}
	return nil
}

func TestAssembleConfinementProvenanceAndFilters(t *testing.T) {
	root := t.TempDir()
	alice := filepath.Join(root, "alice")
	writeFile(t, filepath.Join(alice, "doc.txt"), []byte("hello world"))
	writeFile(t, filepath.Join(alice, "pkg", "a.go"), []byte("package a\n"))
	writeFile(t, filepath.Join(alice, "bin.dat"), []byte{0x00, 0x01, 0x02, 0x00})
	writeFile(t, filepath.Join(alice, "big.txt"), bytes.Repeat([]byte("a"), maxFileBytes+1))
	writeFile(t, filepath.Join(alice, "pkg", ".hidden"), []byte("secret"))
	// A sibling outside alice's scope — confinement must deny crossing into it.
	writeFile(t, filepath.Join(root, "bob", "secret.txt"), []byte("bob's data"))

	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: root, MaxContextBytes: DefaultMaxContextBytes}
	in := Request{
		Prompt: "summarize",
		Paths:  []string{"doc.txt", "pkg", "bin.dat", "big.txt", "../bob/secret.txt", "nope.txt"},
	}

	prompt, items, truncated, err := assemble(context.Background(), envFor(grave, "alice"), in, lim)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if truncated {
		t.Errorf("did not expect truncation under a full budget")
	}

	// Included text files appear in the prompt and are stored in the graveyard.
	if !strings.Contains(prompt, "hello world") || !strings.Contains(prompt, "package a") {
		t.Errorf("prompt missing included file contents:\n%s", prompt)
	}
	doc := itemFor(items, "doc.txt")
	if doc == nil || doc.Ref == "" || doc.Bytes != len("hello world") {
		t.Fatalf("doc.txt item wrong: %+v", doc)
	}
	got, found, err := grave.Get(context.Background(), doc.Ref)
	if err != nil || !found || string(got) != "hello world" {
		t.Errorf("provenance: get(%s) => %q found=%v err=%v", doc.Ref, got, found, err)
	}

	// Filters / confinement.
	if it := itemFor(items, "bin.dat"); it == nil || it.Skipped != "binary" {
		t.Errorf("binary not skipped: %+v", it)
	}
	if it := itemFor(items, "big.txt"); it == nil || it.Skipped != "too-large" {
		t.Errorf("oversize not skipped: %+v", it)
	}
	if it := itemFor(items, "secret.txt"); it == nil || it.Skipped != "denied" {
		t.Errorf("scope escape not denied: %+v", it)
	}
	if it := itemFor(items, "nope.txt"); it == nil || it.Skipped != "missing" {
		t.Errorf("missing path not reported: %+v", it)
	}
	// Dotfiles inside a walked dir are noise and never appear.
	if strings.Contains(prompt, "secret") && !strings.Contains(prompt, "bob") {
		t.Errorf("hidden file leaked into prompt")
	}
}

func TestAssembleSubjectIsolation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alice", "doc.txt"), []byte("alice-only"))

	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: root}
	// bob asks for doc.txt — it resolves under bob's scope, which has no such file.
	_, items, _, err := assemble(context.Background(), envFor(grave, "bob"), Request{Prompt: "x", Paths: []string{"doc.txt"}}, lim)
	if err != nil {
		t.Fatal(err)
	}
	if it := itemFor(items, "doc.txt"); it == nil || it.Skipped == "" {
		t.Fatalf("bob must not reach alice's file: %+v", it)
	}
}

func TestAssembleTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "u", "a.txt"), []byte("12345678")) // 8 bytes, fits
	writeFile(t, filepath.Join(root, "u", "b.txt"), []byte("12345678")) // 8 bytes, over budget

	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: root, MaxContextBytes: 10}
	_, items, truncated, err := assemble(context.Background(), envFor(grave, "u"),
		Request{Prompt: "x", Paths: []string{"a.txt", "b.txt"}}, lim)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatalf("expected truncation, items=%+v", items)
	}
	if it := itemFor(items, "a.txt"); it == nil || it.Skipped != "" {
		t.Errorf("a.txt should be included: %+v", it)
	}
	if it := itemFor(items, "b.txt"); it == nil || it.Skipped != "budget" {
		t.Errorf("b.txt should be budget-skipped: %+v", it)
	}
}

// Inline files are assembled WITHOUT any fs access: text content is included + stored for
// provenance, binary/empty content is skipped, and the byte budget still applies.
func TestAssembleInlineContent(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir(), MaxContextBytes: DefaultMaxContextBytes}
	in := Request{
		Prompt: "summarize",
		Inline: []InlineFile{
			{Path: "me/Notes/spec.md", Content: "# Spec\nhello from samba"},
			{Path: "me/empty.txt", Content: ""},
			{Path: "me/bin.dat", Content: "ab\x00cd"},
		},
	}

	prompt, items, truncated, err := assemble(context.Background(), envFor(grave, "nanu"), in, lim)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if truncated {
		t.Errorf("did not expect truncation")
	}
	if !strings.Contains(prompt, "hello from samba") {
		t.Errorf("prompt missing inline content:\n%s", prompt)
	}
	spec := itemFor(items, "spec.md")
	if spec == nil || spec.Ref == "" || spec.Bytes != len("# Spec\nhello from samba") {
		t.Fatalf("spec item wrong: %+v", spec)
	}
	if got, found, _ := grave.Get(context.Background(), spec.Ref); !found || string(got) != "# Spec\nhello from samba" {
		t.Errorf("inline provenance: get(%s) => %q found=%v", spec.Ref, got, found)
	}
	if it := itemFor(items, "empty.txt"); it == nil || it.Skipped != "empty" {
		t.Errorf("empty inline should be skipped: %+v", it)
	}
	if it := itemFor(items, "bin.dat"); it == nil || it.Skipped != "binary" {
		t.Errorf("binary inline should be skipped: %+v", it)
	}
}

// A single oversized inline file exceeds the budget and is reported as skipped:budget.
func TestAssembleInlineBudget(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir(), MaxContextBytes: 16}
	in := Request{Prompt: "x", Inline: []InlineFile{{Path: "me/big.txt", Content: strings.Repeat("a", 100)}}}
	_, items, truncated, err := assemble(context.Background(), envFor(grave, "nanu"), in, lim)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !truncated {
		t.Errorf("expected truncation")
	}
	if it := itemFor(items, "big.txt"); it == nil || it.Skipped != "budget" {
		t.Errorf("oversized inline should be budget-skipped: %+v", it)
	}
}
