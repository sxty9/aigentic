package aigentic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
)

// recordingGrave counts Put calls and offers a Describer Leitfaden, so a test can prove exactly
// what StoreMode suppresses: the per-run provenance writes and the guidance injection.
type recordingGrave struct {
	puts      int
	leitfaden string
}

func (g *recordingGrave) Put(_ context.Context, _ graveyard.Ref, _ []byte) (graveyard.Ref, error) {
	g.puts++
	return "ref", nil
}
func (g *recordingGrave) Get(context.Context, graveyard.Ref) ([]byte, bool, error) {
	return nil, false, nil
}
func (g *recordingGrave) Describe() string { return g.leitfaden }

// TestStoreModeSuppressesProvenanceAndGuidance is the behaviour-neutrality proof for Stage 2: with
// StoreMode set, assemble writes nothing to the graveyard and injects no substrate guidance, so a
// switch to an owned scheme store cannot pollute the store with run context nor leak its Leitfaden
// into unrelated Ask-AI prompts. Default (provenance) keeps both, exactly as before.
func TestStoreModeSuppressesProvenanceAndGuidance(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alice", "doc.txt"), []byte("hello world"))

	const leitfaden = "STRUKTUR-LEITFADEN: beschreibe jeden Datensatz klar."
	req := Request{Prompt: "summarize", Paths: []string{"doc.txt"}}

	t.Run("provenance (default)", func(t *testing.T) {
		g := &recordingGrave{leitfaden: leitfaden}
		prompt, items, _, err := assemble(context.Background(), envFor(g, "alice"), req, Limits{ContextRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		if g.puts != 1 {
			t.Errorf("expected 1 provenance put, got %d", g.puts)
		}
		if doc := itemFor(items, "doc.txt"); doc == nil || doc.Ref == "" {
			t.Errorf("expected a provenance ref, got %+v", doc)
		}
		if !strings.Contains(prompt, leitfaden) {
			t.Errorf("expected the Leitfaden in the prompt")
		}
	})

	t.Run("store", func(t *testing.T) {
		g := &recordingGrave{leitfaden: leitfaden}
		prompt, items, _, err := assemble(context.Background(), envFor(g, "alice"),
			req, Limits{ContextRoot: root, StoreMode: true})
		if err != nil {
			t.Fatal(err)
		}
		if g.puts != 0 {
			t.Errorf("StoreMode must not write to the graveyard, got %d puts", g.puts)
		}
		if doc := itemFor(items, "doc.txt"); doc == nil || doc.Ref != "" {
			t.Errorf("StoreMode item must carry no provenance ref, got %+v", doc)
		}
		if strings.Contains(prompt, leitfaden) {
			t.Errorf("StoreMode must not inject the Leitfaden:\n%s", prompt)
		}
		// The file content must still reach the model — only provenance/guidance change.
		if !strings.Contains(prompt, "hello world") {
			t.Errorf("context content missing from prompt:\n%s", prompt)
		}
	})
}
