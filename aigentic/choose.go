package aigentic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sxty9/prizm/prizm"
	"github.com/sxty9/prizm/subprizm"
)

// Classifier estimates a request's complexity ("low"|"medium"|"high"). Returning an error
// makes the router fall back to a deterministic heuristic (e.g. when ollama is absent).
type Classifier func(ctx context.Context, in Request) (complexity, reason string, err error)

// ChooseConfig configures the router.
type ChooseConfig struct {
	Classify Classifier  // nil => always use the heuristic (no ollama dependency)
	Policy   RoutePolicy // zero value => defaultPolicy

	// Utilization reports the current Claude subscription utilization (0..1) over its
	// rolling window, with ok=false when unknown/disabled. When the policy picks
	// claude-cli and utilization is at/above SpillAt, the router spills to claude-api to
	// spare subscription headroom for the operator's own dev work. nil => never spill.
	Utilization func() (frac float64, ok bool)
	SpillAt     float64 // spill threshold; 0 => defaultSpillAt (0.80)

	// LocalModels tiers the LOCAL (ollama) model by complexity: when the router picks the
	// ollama leaf and the caller pinned no model, it runs the model configured for that
	// bucket (e.g. a bigger local model for "high"). Empty entries fall back to the ollama
	// leaf's own default. Only the ollama attempt is affected — Claude fallbacks are left
	// with the original request. This is how one host serves a small/large local tier.
	LocalModels LocalModelTier
}

// LocalModelTier maps a complexity bucket to a local (ollama) model tag ("" => leaf default).
type LocalModelTier struct {
	Low    string `json:"low,omitempty"`
	Medium string `json:"medium,omitempty"`
	High   string `json:"high,omitempty"`
}

// pick returns the model tag for a complexity bucket ("" for none/forced).
func (t LocalModelTier) pick(complexity string) string {
	switch complexity {
	case "low":
		return t.Low
	case "high":
		return t.High
	case "medium":
		return t.Medium
	default:
		return ""
	}
}

// defaultPolicy is cli-first: the subscription is a flat-rate cost, so saturate it
// (medium AND high → claude-cli) and reach the metered claude-api only via the
// availability fallback or the subscription-utilization spill. low stays local (ollama).
var defaultPolicy = RoutePolicy{Low: KindOllama, Medium: KindClaudeCLI, High: KindClaudeCLI}

// defaultSpillAt reserves ~20% of the subscription window for dev work: at/above 80%
// utilization, an estimated claude-cli pick spills to claude-api.
const defaultSpillAt = 0.80

var errInvalidComplexity = errors.New("aigentic: classifier returned an invalid complexity")

// complexitySchema is the ollama structured-output schema for the classifier: it forces
// the model to emit exactly {complexity ∈ {low,medium,high}, reason} so even a tiny local
// model (e.g. qwen2.5:0.5b) can't drift into answering the task instead of rating it.
var complexitySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"complexity": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
		"reason":     map[string]any{"type": "string"},
	},
	"required": []string{"complexity", "reason"},
}

func validComplexity(s string) bool { return s == "low" || s == "medium" || s == "high" }

// pick maps a complexity bucket to a leaf kind, falling back to the built-in default for
// any bucket the policy leaves unset.
func (p RoutePolicy) pick(complexity string) prizm.Kind {
	switch complexity {
	case "low":
		if p.Low != "" {
			return p.Low
		}
		return KindOllama
	case "high":
		if p.High != "" {
			return p.High
		}
		return KindClaudeAPI
	default:
		if p.Medium != "" {
			return p.Medium
		}
		return KindClaudeCLI
	}
}

// OllamaClassifier builds a Classifier backed by a cheap, direct ollama call (NOT a
// sub-prizm spawn — the estimate must be cheap and must not run a full leaf path). model
// is the classifier model; "" falls back to the ollama client's default.
func OllamaClassifier(cfg OllamaConfig, model string) Classifier {
	c := newOllamaClient(cfg)
	return func(ctx context.Context, in Request) (string, string, error) {
		m := model
		if m == "" {
			m = c.model
		}
		sys := `Classify the user's task complexity as low, medium, or high. ` +
			`Do not answer or perform the task — only classify it.`
		content, _, err := c.chatFormat(ctx, m, sys, in.Prompt, 80, complexitySchema)
		if err != nil {
			return "", "", err
		}
		var parsed struct {
			Complexity string `json:"complexity"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &parsed); err != nil {
			return "", "", err
		}
		if !validComplexity(parsed.Complexity) {
			return "", "", errInvalidComplexity
		}
		return parsed.Complexity, parsed.Reason, nil
	}
}

// heuristic estimates complexity with no model at all: deterministic, used when no
// classifier is configured or the classifier fails (e.g. ollama absent).
func heuristic(in Request) (complexity, reason string) {
	p := strings.ToLower(in.Prompt)
	for _, k := range []string{"refactor", "architecture", "prove", "design", "optimize", "debug", "analyze"} {
		if strings.Contains(p, k) {
			return "high", "prompt matches a complex-task keyword"
		}
	}
	if len(in.Prompt) < 280 && len(in.Paths) == 0 {
		return "low", "short prompt, no attached paths"
	}
	return "medium", "default heuristic bucket"
}

// fallbackChain returns the picked kind first, then the remaining leaves as availability
// fallbacks in a deterministic order — skipping duplicates and the router itself.
func fallbackChain(picked prizm.Kind) []prizm.Kind {
	out := make([]prizm.Kind, 0, 3)
	seen := map[prizm.Kind]bool{}
	for _, k := range []prizm.Kind{picked, KindOllama, KindClaudeCLI, KindClaudeAPI} {
		if k == "" || k == KindChoose || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// NewChoose returns the router processor (Kind "choose"). It MUST be registered
// WithSpawner(reg) so it can delegate to the leaf kinds; without a spawner it returns
// prizm.ErrNoSpawner.
//
// Algorithm: forced leaf, else classify (ollama) → heuristic fallback → policy pick;
// then forward the SAME request (Choose cleared) to the picked leaf, retrying down an
// availability-fallback chain if a leaf reports ErrProcessorUnavailable. Depth guard and
// correlation id come for free from subprizm.SpawnTyped / the registry.
func NewChoose(cfg ChooseConfig) prizm.Processor {
	policy := cfg.Policy
	if policy == (RoutePolicy{}) {
		policy = defaultPolicy
	}
	spillAt := cfg.SpillAt
	if spillAt <= 0 {
		spillAt = defaultSpillAt
	}

	return prizm.NewTyped(func(ctx context.Context, in Request, env prizm.Env) (Result, error) {
		if err := validate(in); err != nil {
			return Result{}, err
		}
		if env.Spawn == nil {
			return Result{}, prizm.ErrNoSpawner
		}

		// 1) decide which leaf to run.
		pol := policy
		var picked prizm.Kind
		var complexity, reason, source string
		var spilled bool
		var cliUsage float64

		if in.Choose != nil {
			if in.Choose.Policy != nil {
				pol = *in.Choose.Policy
			}
			if in.Choose.Force != "" {
				picked, source = in.Choose.Force, "forced"
			}
		}

		if picked == "" {
			if cfg.Classify != nil {
				if c, r, err := cfg.Classify(ctx, in); err == nil {
					complexity, reason, source = c, r, "ollama-classifier"
				} else {
					complexity, reason, source = heuristicWithSource(in)
				}
			} else {
				complexity, reason, source = heuristicWithSource(in)
			}
			picked = pol.pick(complexity)

			// Spill claude-cli → claude-api when the subscription is near its cap, to
			// keep dev headroom. ollama (low) never spills — it doesn't touch the abo.
			if picked == KindClaudeCLI && cfg.Utilization != nil {
				if frac, ok := cfg.Utilization(); ok && frac >= spillAt {
					picked, spilled, cliUsage = KindClaudeAPI, true, frac
					reason = fmt.Sprintf("cli usage %.0f%% ≥ %.0f%% of window cap; sparing subscription headroom", frac*100, spillAt*100)
				}
			}
		}

		// 2) forward to the picked leaf. A forced pick does NOT fall back (the caller
		//    demanded that engine); an estimated pick walks the availability chain.
		chain := []prizm.Kind{picked}
		if source != "forced" {
			chain = fallbackChain(picked)
		}

		fwd := in
		fwd.Choose = nil // leaves ignore it anyway; keep the forwarded wire clean.

		// Local-model tier: pick a model for the ollama leaf by complexity, but only when the
		// caller pinned none. Applied per-attempt below so a Claude fallback never inherits an
		// ollama model tag.
		tierModel := ""
		if in.Model == "" {
			tierModel = cfg.LocalModels.pick(complexity)
		}

		var lastErr error
		for i, k := range chain {
			attempt := fwd
			if k == KindOllama && tierModel != "" {
				attempt.Model = tierModel
			}
			res, err := subprizm.SpawnTyped[Request, Result](ctx, env.Spawn, env.Header, k, attempt)
			if err != nil {
				if errors.Is(err, ErrProcessorUnavailable) {
					lastErr = err
					continue
				}
				return Result{}, err
			}
			res.Engine = k
			res.Decision = &Decision{
				Picked:     k,
				Complexity: complexity,
				Reason:     reason,
				Source:     source,
				Fallback:   i > 0,
				Spilled:    spilled,
				CLIUsage:   cliUsage,
			}
			return res, nil
		}
		if lastErr == nil {
			lastErr = ErrProcessorUnavailable
		}
		return Result{}, lastErr
	})
}

func heuristicWithSource(in Request) (complexity, reason, source string) {
	c, r := heuristic(in)
	return c, r, "heuristic"
}
