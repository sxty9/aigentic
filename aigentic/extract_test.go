package aigentic_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// captureLeaf is a fake leaf that records the Request it received and returns a fixed answer. It
// stands in for a real engine so a test can assert both the routing decision and that extract handed
// the engine the server-owned transcription instruction (not the caller's raw prompt).
func captureLeaf(kind prizm.Kind, got *aigentic.Request, answer string) prizm.Processor {
	return prizm.NewTyped(func(_ context.Context, in aigentic.Request, _ prizm.Env) (aigentic.Result, error) {
		if got != nil {
			*got = in
		}
		return aigentic.Result{Output: answer, Engine: kind, Model: string(kind) + "-model"}, nil
	})
}

// routeExtract registers the given fake leaves + the choose router + the extract router, then runs
// one extract request through the P-layer entry point (the same door the MCP tool uses).
func routeExtract(t *testing.T, cfg aigentic.ChooseConfig, leaves map[prizm.Kind]prizm.Processor, req aigentic.Request) (aigentic.Result, error) {
	t.Helper()
	grave := graveyard.NewMemory()
	reg := prizm.NewRegistry(0)
	for kind, proc := range leaves {
		if err := reg.Register(kind, prizm.NewPrizm(proc, grave)); err != nil {
			t.Fatalf("register %s: %v", kind, err)
		}
	}
	if err := reg.Register(aigentic.KindChoose, prizm.NewPrizm(aigentic.NewChoose(cfg), grave, prizm.WithSpawner(reg))); err != nil {
		t.Fatalf("register choose: %v", err)
	}
	if err := reg.Register(aigentic.KindExtract, prizm.NewPrizm(aigentic.NewExtract(), grave, prizm.WithSpawner(reg))); err != nil {
		t.Fatalf("register extract: %v", err)
	}
	return aigentic.Extract(context.Background(), reg, "tester", req)
}

func pdfFile(path, text string) aigentic.InlineFile {
	return aigentic.InlineFile{Path: path, Content: base64.StdEncoding.EncodeToString([]byte(text)), MediaType: "application/pdf"}
}

// TestExtractTextLayerDocument: a PDF with a readable text layer is routed to a PDF-capable engine,
// which returns the layer's text — and extract fed that engine the server-owned transcription
// instruction, not the caller's (empty) prompt.
func TestExtractTextLayerDocument(t *testing.T) {
	var got aigentic.Request
	cfg := aigentic.ChooseConfig{
		Classify: fixedClassifier("medium"), // medium => policy picks claude-cli
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindClaudeCLI: captureLeaf(aigentic.KindClaudeCLI, &got, "PLANT DATA — model X, serial 42"),
	}
	req := aigentic.Request{Inline: []aigentic.InlineFile{pdfFile("specs/plate.pdf", "%PDF text layer")}}
	res, err := routeExtract(t, cfg, leaves, req)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Output != "PLANT DATA — model X, serial 42" {
		t.Errorf("output = %q, want the engine's transcription", res.Output)
	}
	if res.Engine != aigentic.KindClaudeCLI {
		t.Errorf("engine = %q, want claude-cli", res.Engine)
	}
	if res.Model == "" {
		t.Error("model is empty; the extract must report which model read the file (labelling axiom)")
	}
	// The engine received the server-owned instruction and the PDF — not a hand-rolled caller prompt.
	if !strings.Contains(got.Prompt, "Transcribe every piece of text") {
		t.Errorf("engine prompt did not carry the extraction instruction: %q", got.Prompt)
	}
	if len(got.Inline) != 1 || got.Inline[0].Path != "specs/plate.pdf" {
		t.Errorf("engine did not receive the PDF attachment: %+v", got.Inline)
	}
}

// TestExtractImageWithText: an image (a photo of a nameplate) is routed to a vision-capable engine,
// never to a blind local model, and its recognized text comes back.
func TestExtractImageWithText(t *testing.T) {
	var ollamaCalled bool
	var visionGot aigentic.Request
	cfg := aigentic.ChooseConfig{
		Classify:      fixedClassifier("low"), // low => policy picks ollama (blind here)
		VisionForKind: visionFor(map[prizm.Kind]bool{aigentic.KindClaudeCLI: true}),
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama:    flagLeaf(aigentic.KindOllama, &ollamaCalled, "invented description"),
		aigentic.KindClaudeCLI: captureLeaf(aigentic.KindClaudeCLI, &visionGot, "Siemens 3RT2015 — 24VDC"),
	}
	req := aigentic.Request{Inline: []aigentic.InlineFile{{
		Path: "uploads/plate.jpg", Content: base64.StdEncoding.EncodeToString([]byte("JPEGDATA")), MediaType: "image/jpeg",
	}}}
	res, err := routeExtract(t, cfg, leaves, req)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ollamaCalled {
		t.Fatal("the blind ollama leaf read an image extract — it must be kept off image requests")
	}
	if res.Engine != aigentic.KindClaudeCLI || res.Output != "Siemens 3RT2015 — 24VDC" {
		t.Errorf("engine = %q output = %q; want the vision engine's transcription", res.Engine, res.Output)
	}
	if len(visionGot.Inline) != 1 || visionGot.Inline[0].MediaType != "image/jpeg" {
		t.Errorf("vision engine did not receive the image bytes: %+v", visionGot.Inline)
	}
}

// TestExtractFailureNoVisionEngine: an image extract with no vision-capable engine in reach is a
// NAMED failure (ErrNoVisionEngine), never a blind model's invented transcription. Because extract
// is stateless, the caller re-runs it by calling again — no re-upload needed.
func TestExtractFailureNoVisionEngine(t *testing.T) {
	var ollamaCalled bool
	cfg := aigentic.ChooseConfig{
		Classify:      fixedClassifier("low"),
		VisionForKind: visionFor(map[prizm.Kind]bool{}), // nothing can see images
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama: flagLeaf(aigentic.KindOllama, &ollamaCalled, "invented description"),
	}
	req := aigentic.Request{Inline: []aigentic.InlineFile{{
		Path: "uploads/scan.png", Content: base64.StdEncoding.EncodeToString([]byte("PNGDATA")), MediaType: "image/png",
	}}}
	_, err := routeExtract(t, cfg, leaves, req)
	if !errors.Is(err, aigentic.ErrNoVisionEngine) {
		t.Fatalf("error = %v, want ErrNoVisionEngine", err)
	}
	if ollamaCalled {
		t.Fatal("a blind model was asked to transcribe an image it cannot see")
	}
}

// TestExtractRequiresFile: extract without any attachment is an invalid request, not a run against
// an empty document.
func TestExtractRequiresFile(t *testing.T) {
	cfg := aigentic.ChooseConfig{Classify: fixedClassifier("low")}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama: flagLeaf(aigentic.KindOllama, new(bool), "unused"),
	}
	_, err := routeExtract(t, cfg, leaves, aigentic.Request{Prompt: "read it"})
	if !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}
