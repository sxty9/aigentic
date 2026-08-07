package aigentic

import (
	"context"
	"os"

	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// Config bundles everything needed to build the four aigentic processors.
type Config struct {
	MaxTokens       int    // answer-token ceiling (token-overusage guard); 0 => DefaultMaxTokens
	MaxContextBytes int    // Paths context budget; 0 => DefaultMaxContextBytes
	ContextRoot     string // allowlisted root for Paths; "" => $AIGENTIC_CONTEXT_ROOT, then DefaultContextRoot
	Ollama          OllamaConfig
	ClaudeCLI       ClaudeCLIConfig
	ClaudeAPI       ClaudeAPIConfig
	Choose          ChooseConfig
	// OnUsage, when set, receives the token accounting of every LEAF run (ollama / claude-cli /
	// claude-api), including the leaf a choose call resolves to — the choose router itself is not
	// metered, so a routed run counts once. It is the consumption interface's ingestion point (the
	// daemon wires it to the usage reporter). nil => no metering. It runs on the request path, so
	// the sink must be cheap and non-blocking.
	OnUsage func(engine prizm.Kind, u Usage)
}

// limits derives the server-authoritative guards from Config (with env/default fallback).
func (cfg Config) limits() Limits {
	root := cfg.ContextRoot
	if root == "" {
		root = os.Getenv("AIGENTIC_CONTEXT_ROOT")
	}
	if root == "" {
		root = DefaultContextRoot
	}
	return Limits{
		MaxTokens:       cfg.MaxTokens,
		MaxContextBytes: cfg.MaxContextBytes,
		ContextRoot:     root,
		StoreMode:       os.Getenv("AIGENTIC_GRAVE_MODE") == "store",
	}
}

// Register builds the four processors over grave and registers them under their kinds.
// The choose router is handed the registry as its spawner (WithSpawner) so it can
// delegate to the three leaves through the same registry — the canonical prizm pattern.
//
// This is the single wiring point the daemon (R/shell) calls; the round-trip test calls
// it too, which is exactly why one shared In/Out across all four kinds is provable here
// without any HTTP surface.
func Register(reg *prizm.Registry, grave graveyard.Graveyard, cfg Config) error {
	lim := cfg.limits()
	leaves := []struct {
		kind prizm.Kind
		proc prizm.Processor
	}{
		{KindOllama, NewOllama(cfg.Ollama, lim)},
		{KindClaudeCLI, NewClaudeCLI(cfg.ClaudeCLI, lim)},
		{KindClaudeAPI, NewClaudeAPI(cfg.ClaudeAPI, lim)},
	}
	for _, l := range leaves {
		proc := l.proc
		if cfg.OnUsage != nil {
			// Meter the leaf, not the router: a choose call spawns its picked leaf THROUGH the
			// registry, so the leaf's metered run is the one that counts (choose forwards the same
			// Usage, but is left unmetered to avoid double-counting).
			proc = meterProcessor{inner: proc, kind: l.kind, sink: cfg.OnUsage}
		}
		if err := reg.Register(l.kind, prizm.NewPrizm(proc, grave)); err != nil {
			return err
		}
	}
	if err := reg.Register(KindChoose, prizm.NewPrizm(NewChoose(cfg.Choose), grave, prizm.WithSpawner(reg))); err != nil {
		return err
	}
	// extract is a router over choose (it forwards the file-bearing request through the same
	// vision-aware path), so it too needs the registry as its spawner. It is not metered: the leaf
	// choose resolves to is, so an extraction run counts once — like a routed ask.
	return reg.Register(KindExtract, prizm.NewPrizm(NewExtract(), grave, prizm.WithSpawner(reg)))
}

// meterProcessor wraps a leaf processor to report its token Usage to a sink after each successful
// run. It decodes the response in the P layer — package aigentic owns Result — so the HTTP shell
// keeps its OSI-switch property and never decodes Data itself.
type meterProcessor struct {
	inner prizm.Processor
	kind  prizm.Kind
	sink  func(prizm.Kind, Usage)
}

func (m meterProcessor) Process(ctx context.Context, req prizm.Request, env prizm.Env) (prizm.Response, error) {
	resp, err := m.inner.Process(ctx, req, env)
	if err != nil {
		return resp, err
	}
	if res, derr := prizm.DecodeData[Result](resp.Data); derr == nil {
		if res.Usage.InputTokens > 0 || res.Usage.OutputTokens > 0 {
			m.sink(m.kind, res.Usage)
		}
	}
	return resp, err
}
