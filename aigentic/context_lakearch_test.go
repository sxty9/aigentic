//go:build lakearch

package aigentic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxty9/aigentic/graveyard/lakegrave"
)

// TestAssembleStoresInLakearch proves the G legs join: the path-context assembler (G2)
// stores file bytes in the real lakearch substrate (G1) and the provenance Ref resolves
// back to the exact content through lakearch's gate.
func TestAssembleStoresInLakearch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alice", "doc.txt"), []byte("provenance in lakearch"))

	k, err := lakegrave.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open lakearch: %v", err)
	}
	defer k.Close()

	prompt, items, _, err := assemble(context.Background(), envFor(k, "alice"),
		Request{Prompt: "summarize", Paths: []string{"doc.txt"}}, Limits{ContextRoot: root})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	doc := itemFor(items, "doc.txt")
	if doc == nil || doc.Ref == "" {
		t.Fatalf("no provenance ref: %+v", doc)
	}
	got, found, err := k.Get(context.Background(), doc.Ref)
	if err != nil || !found || string(got) != "provenance in lakearch" {
		t.Fatalf("lakearch get(%s) => %q found=%v err=%v", doc.Ref, got, found, err)
	}
	if !strings.Contains(prompt, "provenance in lakearch") {
		t.Errorf("prompt missing file content:\n%s", prompt)
	}
}
