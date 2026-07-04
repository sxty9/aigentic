package aigentic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
)

// describingGrave is a memory graveyard that also implements Describer — the shape
// the scheme backend has. It proves the structure-guidance injection without cgo.
type describingGrave struct {
	graveyard.Graveyard
}

func (describingGrave) Describe() string {
	return "GUIDANCE-MARKER: the data is clearly structured and must be described that way"
}

// TestAssembleInjectsSubstrateGuidance proves that a Describer graveyard's guidance
// is prepended to the assembled prompt (which every engine receives).
func TestAssembleInjectsSubstrateGuidance(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "u", "doc.txt"), []byte("hi"))

	g := describingGrave{graveyard.NewMemory()}
	prompt, _, _, err := assemble(context.Background(), envFor(g, "u"),
		Request{Prompt: "summarize", Paths: []string{"doc.txt"}}, Limits{ContextRoot: root})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.HasPrefix(prompt, "GUIDANCE-MARKER") {
		t.Fatalf("substrate guidance not injected as preamble:\n%s", prompt)
	}
	if !strings.Contains(prompt, "summarize") {
		t.Fatalf("instruction missing from prompt:\n%s", prompt)
	}
}

// TestComposeCLIPromptInjectsGuidance proves the claude-cli leaf (which bypasses
// assemble/composePrompt) still gets the substrate guidance as a preamble.
func TestComposeCLIPromptInjectsGuidance(t *testing.T) {
	p := composeCLIPrompt("GUIDANCE-MARKER", "", Request{Prompt: "do it"})
	if !strings.HasPrefix(p, "GUIDANCE-MARKER") {
		t.Fatalf("cli prompt missing guidance preamble:\n%s", p)
	}
	if !strings.Contains(p, "do it") {
		t.Fatalf("cli prompt missing instruction:\n%s", p)
	}
	// No guidance (non-scheme backend) => no preamble, prompt starts with the work.
	if p2 := composeCLIPrompt("", "", Request{Prompt: "x"}); !strings.HasPrefix(p2, "x") {
		t.Fatalf("empty guidance should add no preamble:\n%q", p2)
	}
}

// TestAssembleNoGuidanceForPlainGrave proves a backend without Describer (memory,
// lakearch) injects nothing — the capability is opt-in.
func TestAssembleNoGuidanceForPlainGrave(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "u", "doc.txt"), []byte("hi"))

	prompt, _, _, err := assemble(context.Background(), envFor(graveyard.NewMemory(), "u"),
		Request{Prompt: "x", Paths: []string{"doc.txt"}}, Limits{ContextRoot: root})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(prompt, "GUIDANCE-MARKER") {
		t.Fatalf("a plain graveyard must not inject guidance:\n%s", prompt)
	}
}
