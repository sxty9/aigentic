package aigentic_test

import (
	"context"
	"testing"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// recordingOllama is a fake ollama leaf that captures the model each request carries.
func recordingOllama(got *string) prizm.Processor {
	return prizm.NewTyped(func(_ context.Context, in aigentic.Request, _ prizm.Env) (aigentic.Result, error) {
		*got = in.Model
		return aigentic.Result{Output: "ok", Engine: aigentic.KindOllama, Model: in.Model}, nil
	})
}

func fixedClassifier(complexity string) aigentic.Classifier {
	return func(_ context.Context, _ aigentic.Request) (string, string, error) {
		return complexity, "fixed", nil
	}
}

// routeChoose builds a registry with a recording ollama leaf + the choose router (cfg) and
// routes one request, returning the model the ollama leaf saw.
func routeChoose(t *testing.T, cfg aigentic.ChooseConfig, req aigentic.Request) string {
	t.Helper()
	grave := graveyard.NewMemory()
	reg := prizm.NewRegistry(0)
	var seen string
	if err := reg.Register(aigentic.KindOllama, prizm.NewPrizm(recordingOllama(&seen), grave)); err != nil {
		t.Fatalf("register ollama: %v", err)
	}
	if err := reg.Register(aigentic.KindChoose, prizm.NewPrizm(aigentic.NewChoose(cfg), grave, prizm.WithSpawner(reg))); err != nil {
		t.Fatalf("register choose: %v", err)
	}
	data, err := prizm.EncodeData(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := reg.Route(context.Background(), prizm.Request{Header: prizm.Header{Kind: aigentic.KindChoose}, Data: data}); err != nil {
		t.Fatalf("route: %v", err)
	}
	return seen
}

func TestLocalModelTierRouting(t *testing.T) {
	localPolicy := aigentic.RoutePolicy{Low: aigentic.KindOllama, Medium: aigentic.KindOllama, High: aigentic.KindOllama}
	tier := aigentic.LocalModelTier{High: "qwen2.5:32b"} // medium/low fall back to the leaf default ("")

	// A "high" task gets the high-tier local model.
	if got := routeChoose(t, aigentic.ChooseConfig{
		Classify: fixedClassifier("high"), Policy: localPolicy, LocalModels: tier,
	}, aigentic.Request{Prompt: "prove this theorem"}); got != "qwen2.5:32b" {
		t.Errorf("high tier model = %q, want qwen2.5:32b", got)
	}

	// A "medium" task has no tier model → leaf default ("").
	if got := routeChoose(t, aigentic.ChooseConfig{
		Classify: fixedClassifier("medium"), Policy: localPolicy, LocalModels: tier,
	}, aigentic.Request{Prompt: "summarise"}); got != "" {
		t.Errorf("medium tier model = %q, want empty (leaf default)", got)
	}

	// An explicit per-request Model always wins over the tier.
	if got := routeChoose(t, aigentic.ChooseConfig{
		Classify: fixedClassifier("high"), Policy: localPolicy, LocalModels: tier,
	}, aigentic.Request{Prompt: "prove this", Model: "pinned-model"}); got != "pinned-model" {
		t.Errorf("explicit model = %q, want pinned-model (must win over tier)", got)
	}
}
