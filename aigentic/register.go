package aigentic

import (
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
		if err := reg.Register(l.kind, prizm.NewPrizm(l.proc, grave)); err != nil {
			return err
		}
	}
	return reg.Register(KindChoose, prizm.NewPrizm(NewChoose(cfg.Choose), grave, prizm.WithSpawner(reg)))
}
