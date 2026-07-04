//go:build scheme

package schemegrave

import (
	"context"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
)

// TestSchemegraveRoundtrip proves the scheme substrate satisfies the graveyard seam
// (Put/Get) plus the mutable capabilities (Delete/List) and scheme's richer surface
// (PutStructured/Move/SetDescription/Describe) through the C-ABI FFI.
func TestSchemegraveRoundtrip(t *testing.T) {
	ctx := context.Background()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	// PutStructured honors the path and the mandatory description; Get reads it back.
	ref, err := b.PutStructured(ctx, "raum/hardware/computer/anleitung.pdf",
		"Bedienungsanleitung des Computers", []byte("PDF-BYTES"))
	if err != nil {
		t.Fatalf("put_structured: %v", err)
	}
	if ref != "raum/hardware/computer/anleitung.pdf" {
		t.Fatalf("ref = %q, want the input path", ref)
	}
	got, found, err := b.Get(ctx, ref)
	if err != nil || !found || string(got) != "PDF-BYTES" {
		t.Fatalf("get(%s) => %q found=%v err=%v", ref, got, found, err)
	}

	// Empty description is rejected (§4).
	if _, err := b.PutStructured(ctx, "raum/x.txt", "   ", []byte("y")); err == nil {
		t.Fatalf("empty description must be rejected")
	}

	// Base Put honors the ref as a path (mutable overwrite).
	if _, err := b.Put(ctx, "raum/notiz.txt", []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := b.Put(ctx, "raum/notiz.txt", []byte("v2")); err != nil {
		t.Fatalf("put overwrite: %v", err)
	}
	if got, _, _ := b.Get(ctx, "raum/notiz.txt"); string(got) != "v2" {
		t.Fatalf("overwrite: got %q, want v2", got)
	}

	// Base Put with an empty ref lands in eingang/ and returns the assigned path.
	eref, err := b.Put(ctx, "", []byte("lose bytes"))
	if err != nil {
		t.Fatalf("put eingang: %v", err)
	}
	if !strings.HasPrefix(string(eref), "eingang/") {
		t.Fatalf("empty ref landed at %q, want eingang/…", eref)
	}

	// List enumerates file paths under a prefix (Listable).
	refs, err := b.List(ctx, "raum")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := map[graveyard.Ref]bool{
		"raum/hardware/computer/anleitung.pdf": true,
		"raum/notiz.txt":                       true,
	}
	for _, r := range refs {
		delete(want, r)
	}
	if len(want) != 0 {
		t.Fatalf("list missing entries: %v (got %v)", want, refs)
	}

	// Move re-keys; content and description survive.
	if err := b.Move(ctx, "raum/notiz.txt", "archiv/notiz.txt"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, found, _ := b.Get(ctx, "raum/notiz.txt"); found {
		t.Fatalf("source still present after move")
	}
	if got, found, _ := b.Get(ctx, "archiv/notiz.txt"); !found || string(got) != "v2" {
		t.Fatalf("moved content = %q found=%v", got, found)
	}

	// Delete is idempotent (Deletable).
	if err := b.Delete(ctx, "archiv/notiz.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := b.Delete(ctx, "archiv/notiz.txt"); err != nil {
		t.Fatalf("delete idempotent: %v", err)
	}
	if _, found, _ := b.Get(ctx, "archiv/notiz.txt"); found {
		t.Fatalf("deleted node still present")
	}

	// SetDescription clears the undescribed mark of the eingang node.
	if err := b.SetDescription(ctx, string(eref), "jetzt klar eingeordnet"); err != nil {
		t.Fatalf("set_description: %v", err)
	}

	// Describe returns the structure-guidance Leitfaden (§9).
	guidance := b.Describe()
	if !strings.Contains(guidance, "klar strukturiert") || !strings.Contains(guidance, "Beschreibung") {
		t.Fatalf("Describe() missing guidance: %q", guidance)
	}
}

// TestSchemegraveGetAbsent confirms absence is a normal outcome (found=false).
func TestSchemegraveGetAbsent(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()
	if _, found, err := b.Get(context.Background(), "gibt/es/nicht.txt"); found || err != nil {
		t.Fatalf("absent get => found=%v err=%v, want found=false nil", found, err)
	}
}
