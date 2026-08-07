package aigentic_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

func imageReq(prompt string) aigentic.Request {
	return aigentic.Request{
		Prompt: prompt,
		Inline: []aigentic.InlineFile{{Path: "room/photo.png", Content: base64.StdEncoding.EncodeToString([]byte("PNGDATA")), MediaType: "image/png"}},
	}
}

// flagLeaf is a fake leaf that records whether it was invoked and returns a fixed answer. The
// "answer" stands in for the fabricated description a blind model would produce for an image.
func flagLeaf(kind prizm.Kind, called *bool, answer string) prizm.Processor {
	return prizm.NewTyped(func(_ context.Context, _ aigentic.Request, _ prizm.Env) (aigentic.Result, error) {
		*called = true
		return aigentic.Result{Output: answer, Engine: kind}, nil
	})
}

// routeVision registers the given fake leaves + the choose router and routes one request, returning
// the Result and error. leaves maps a kind to its processor; a kind absent from the map is simply not
// registered (so the router sees it as unavailable).
func routeVision(t *testing.T, cfg aigentic.ChooseConfig, leaves map[prizm.Kind]prizm.Processor, req aigentic.Request) (aigentic.Result, error) {
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
	res, err := aigentic.Route(context.Background(), reg, aigentic.KindChoose, "tester", req)
	return res, err
}

// visionFor builds a VisionForKind that reports the given capability per kind (absent => not capable).
func visionFor(caps map[prizm.Kind]bool) func(context.Context, prizm.Kind, string) bool {
	return func(_ context.Context, k prizm.Kind, _ string) bool { return caps[k] }
}

// TestRouterImageOnlyBlindModelRefuses is the core case measured in the bug report: an image request
// with only a text-only model in reach must produce a NAMED refusal, never the model's invented
// description. The blind ollama leaf must not even be invoked.
func TestRouterImageOnlyBlindModelRefuses(t *testing.T) {
	var ollamaCalled bool
	cfg := aigentic.ChooseConfig{
		Classify: fixedClassifier("low"), // low => policy picks ollama
		// No vision anywhere: the local model is text-only and no Claude access is configured.
		VisionForKind: visionFor(map[prizm.Kind]bool{}),
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama: flagLeaf(aigentic.KindOllama, &ollamaCalled, "The photos show 1. a projector 2. a screen 3. a table"),
	}
	_, err := routeVision(t, cfg, leaves, imageReq("Which devices are used here?"))
	if !errors.Is(err, aigentic.ErrNoVisionEngine) {
		t.Fatalf("error = %v, want ErrNoVisionEngine", err)
	}
	if ollamaCalled {
		t.Fatal("the blind ollama leaf was invoked for an image request — it must be kept off it entirely")
	}
}

// TestRouterImageRoutesToVisionEngine: when the complexity pick is a blind local model but a
// vision-capable engine is available, the image request goes to the capable engine, not the blind one.
func TestRouterImageRoutesToVisionEngine(t *testing.T) {
	var ollamaCalled, cliCalled bool
	cfg := aigentic.ChooseConfig{
		Classify:      fixedClassifier("low"), // low => policy picks ollama (blind)
		VisionForKind: visionFor(map[prizm.Kind]bool{aigentic.KindClaudeCLI: true}),
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama:    flagLeaf(aigentic.KindOllama, &ollamaCalled, "blind description"),
		aigentic.KindClaudeCLI: flagLeaf(aigentic.KindClaudeCLI, &cliCalled, "a real answer"),
	}
	res, err := routeVision(t, cfg, leaves, imageReq("Which devices are used here?"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Engine != aigentic.KindClaudeCLI {
		t.Errorf("engine = %q, want claude-cli (the vision-capable engine)", res.Engine)
	}
	if ollamaCalled {
		t.Error("the blind ollama leaf was invoked despite a vision engine being available")
	}
	if !cliCalled {
		t.Error("the vision-capable claude-cli leaf was not invoked")
	}
}

// TestRouterDefaultVisionCapability: with no VisionForKind wired, the safe default treats the Claude
// leaves as vision-capable and any local model as blind, so an image request still avoids ollama.
func TestRouterDefaultVisionCapability(t *testing.T) {
	var ollamaCalled, cliCalled bool
	cfg := aigentic.ChooseConfig{Classify: fixedClassifier("low")} // VisionForKind nil => default
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama:    flagLeaf(aigentic.KindOllama, &ollamaCalled, "blind description"),
		aigentic.KindClaudeCLI: flagLeaf(aigentic.KindClaudeCLI, &cliCalled, "a real answer"),
	}
	res, err := routeVision(t, cfg, leaves, imageReq("Which devices are used here?"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Engine != aigentic.KindClaudeCLI || ollamaCalled {
		t.Errorf("engine = %q, ollamaCalled = %v; want claude-cli and ollama untouched", res.Engine, ollamaCalled)
	}
	_ = cliCalled
}

// TestRouterForcedBlindModelWithImageRefuses: pinning the blind local model for an image request is
// still a named refusal — the force cannot override the hard capability precondition.
func TestRouterForcedBlindModelWithImageRefuses(t *testing.T) {
	var ollamaCalled bool
	cfg := aigentic.ChooseConfig{VisionForKind: visionFor(map[prizm.Kind]bool{})}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama: flagLeaf(aigentic.KindOllama, &ollamaCalled, "blind description"),
	}
	req := imageReq("Which devices are used here?")
	req.Choose = &aigentic.ChooseOptions{Force: aigentic.KindOllama}
	_, err := routeVision(t, cfg, leaves, req)
	if !errors.Is(err, aigentic.ErrNoVisionEngine) {
		t.Fatalf("error = %v, want ErrNoVisionEngine", err)
	}
	if ollamaCalled {
		t.Fatal("a forced blind model still saw the image request")
	}
}

// TestRouterTextRequestUnaffected: a request WITHOUT images routes exactly as before (the vision
// precondition applies only when images are present).
func TestRouterTextRequestUnaffected(t *testing.T) {
	var ollamaCalled bool
	cfg := aigentic.ChooseConfig{
		Classify:      fixedClassifier("low"),
		VisionForKind: visionFor(map[prizm.Kind]bool{}), // no vision anywhere, but no images either
	}
	leaves := map[prizm.Kind]prizm.Processor{
		aigentic.KindOllama: flagLeaf(aigentic.KindOllama, &ollamaCalled, "text answer"),
	}
	res, err := routeVision(t, cfg, leaves, aigentic.Request{Prompt: "summarise this"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Engine != aigentic.KindOllama || !ollamaCalled {
		t.Errorf("engine = %q, ollamaCalled = %v; want ollama to answer a plain text request", res.Engine, ollamaCalled)
	}
}

// --- ollama leaf: capability is read from the model, images are delivered to a vision model ---

// fakeOllama serves /api/show (capabilities) and /api/chat, recording whether the chat carried images.
type fakeOllama struct {
	srv        *httptest.Server
	visionCaps bool // what /api/show advertises for any model
	chatCalled bool
	gotImages  bool
}

func newFakeOllama(t *testing.T, vision bool) *fakeOllama {
	t.Helper()
	f := &fakeOllama{visionCaps: vision}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			caps := []string{"completion"}
			if f.visionCaps {
				caps = append(caps, "vision")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
		case "/api/chat":
			f.chatCalled = true
			var body struct {
				Messages []struct {
					Images []string `json:"images"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, m := range body.Messages {
				if len(m.Images) > 0 {
					f.gotImages = true
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]string{"content": "ok"}, "prompt_eval_count": 1, "eval_count": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// routeOllama registers the ollama leaf pointed at f and routes one request directly to it.
func routeOllama(t *testing.T, f *fakeOllama, req aigentic.Request) (aigentic.Result, error) {
	t.Helper()
	grave := graveyard.NewMemory()
	reg := prizm.NewRegistry(0)
	cfg := aigentic.OllamaConfig{BaseURL: f.srv.URL, Model: "local-model"}
	if err := reg.Register(aigentic.KindOllama, prizm.NewPrizm(aigentic.NewOllama(cfg, aigentic.Limits{}), grave)); err != nil {
		t.Fatalf("register ollama: %v", err)
	}
	return aigentic.Route(context.Background(), reg, aigentic.KindOllama, "tester", req)
}

// TestOllamaLeafRefusesImageOnTextModel: the leaf reads the model's advertised capabilities and
// refuses an image request when the model has no vision — the chat endpoint is never called.
func TestOllamaLeafRefusesImageOnTextModel(t *testing.T) {
	f := newFakeOllama(t, false) // text-only model
	_, err := routeOllama(t, f, imageReq("What is in this photo?"))
	if !errors.Is(err, aigentic.ErrNoVisionEngine) {
		t.Fatalf("error = %v, want ErrNoVisionEngine", err)
	}
	if f.chatCalled {
		t.Fatal("the text model was asked to answer an image request")
	}
}

// TestOllamaLeafDeliversImagesToVisionModel: a vision model qualifies (probed, not assumed) and
// actually receives the image bytes on the chat message.
func TestOllamaLeafDeliversImagesToVisionModel(t *testing.T) {
	f := newFakeOllama(t, true) // vision-capable model
	res, err := routeOllama(t, f, imageReq("What is in this photo?"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !f.chatCalled || !f.gotImages {
		t.Fatalf("chatCalled=%v gotImages=%v; want the vision model to be called WITH images", f.chatCalled, f.gotImages)
	}
	if res.Engine != aigentic.KindOllama {
		t.Errorf("engine = %q, want ollama", res.Engine)
	}
}

// TestOllamaLeafNamesUnreadPDF: the local engine never reads PDFs, so a PDF attachment is named as
// unread IN THE ANSWER rather than silently dropped (a PDF-only request needs no vision probe).
func TestOllamaLeafNamesUnreadPDF(t *testing.T) {
	f := newFakeOllama(t, false)
	req := aigentic.Request{
		Prompt: "Summarise the attached document.",
		Inline: []aigentic.InlineFile{{Path: "specs/report.pdf", Content: base64.StdEncoding.EncodeToString([]byte("%PDF-1.7")), MediaType: "application/pdf"}},
	}
	res, err := routeOllama(t, f, req)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !strings.Contains(res.Output, "report.pdf") || !strings.Contains(res.Output, "could not read") {
		t.Fatalf("answer did not name the unread PDF: %q", res.Output)
	}
}
