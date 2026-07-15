package aigentic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sxty9/prizm/prizm"
)

// OllamaConfig configures the local-ollama leaf. BaseURL/Client are injectable so tests
// can point at an httptest server — no real ollama daemon required.
type OllamaConfig struct {
	BaseURL string       // default "http://localhost:11434"
	Model   string       // default model when Request.Model is empty
	Client  *http.Client // default http.DefaultClient
	// CtxCap returns the context window (num_ctx / KV cache) to request from ollama — a FIXED
	// size, wired to a live setting (e.g. a context-mode toggle). nil => defaultMaxCtx. It is
	// deliberately fixed, NOT sized per prompt: ollama reloads the model whenever num_ctx
	// changes, so a varying value thrashes the model in and out of VRAM across a multi-request
	// workload (e.g. a crawl's many decisions) — a fixed size keeps exactly one warm runner.
	CtxCap func() int
}

// defaultMaxCtx is the context window used when no CtxCap is configured (fits a 14b in ~10 GB).
const defaultMaxCtx = 12288

// ollamaClient is the minimal /api/chat client, shared by the ollama leaf and the
// choose router's classifier (so the cheap classification call reuses one code path).
type ollamaClient struct {
	base   string
	model  string
	client *http.Client
	ctxCap func() int

	mu        sync.Mutex
	autoModel string // lazily-detected model when none is configured (zero-config)
}

func newOllamaClient(cfg OllamaConfig) *ollamaClient {
	base := cfg.BaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	// OLLAMA_HOST conventionally carries a bare host:port (no scheme); we use it as a
	// URL base, so default the scheme to http when one is absent.
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	return &ollamaClient{base: base, model: cfg.Model, client: client, ctxCap: cfg.CtxCap}
}

// numCtx returns the context window (num_ctx / KV cache) for a request: a FIXED size from the
// configured cap (a live context-mode setting) or defaultMaxCtx. It is deliberately NOT sized
// per prompt — ollama reloads the model whenever num_ctx changes, so a varying value would thrash
// the model in and out of VRAM across a multi-request workload (a crawl reloaded on every page and
// never finished). A fixed size keeps exactly one warm runner; the smaller value (vs the model's
// 32k default) just keeps the footprint lean. contextMode picks the fixed size, not per-prompt.
func (c *ollamaClient) numCtx() int {
	if c.ctxCap != nil {
		if v := c.ctxCap(); v > 0 {
			return v
		}
	}
	return defaultMaxCtx
}

// chat issues a non-streaming /api/chat call and returns the assistant content + usage.
func (c *ollamaClient) chat(ctx context.Context, model, system, user string, numPredict int) (string, Usage, error) {
	return c.chatFormat(ctx, model, system, user, numPredict, nil)
}

// chatFormat is chat with an optional ollama structured-output schema. When format is
// non-nil it constrains the model to emit JSON matching that schema (so even a tiny model
// follows the shape) and pins temperature to 0 for a deterministic estimate; the plain
// leaf path passes nil and stays free-form.
func (c *ollamaClient) chatFormat(ctx context.Context, model, system, user string, numPredict int, format any) (string, Usage, error) {
	resolved, err := c.resolveModel(ctx, model)
	if err != nil {
		// No model to run (none configured and none pulled) is unavailability, not a hard
		// failure — so the choose router falls back to another engine.
		return "", Usage{}, fmt.Errorf("%w: ollama: %v", ErrProcessorUnavailable, err)
	}
	model = resolved
	msgs := make([]map[string]string, 0, 2)
	if system != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": user})

	options := map[string]any{"num_predict": numPredict, "num_ctx": c.numCtx()}
	payload := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   false,
		"options":  options,
	}
	if format != nil {
		payload["format"] = format
		options["temperature"] = 0
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// A dial error (no ollama installed/running) is unavailability, not a bad request.
		return "", Usage{}, fmt.Errorf("%w: ollama: %v", ErrProcessorUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Model not pulled = this engine can't serve the request = unavailable, so choose
		// falls back rather than surfacing a hard 502.
		return "", Usage{}, fmt.Errorf("%w: ollama model %q not found (pull it first)", ErrProcessorUnavailable, model)
	case resp.StatusCode != http.StatusOK:
		return "", Usage{}, fmt.Errorf("%w: ollama: status %d", ErrProcessorUnavailable, resp.StatusCode)
	}

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	u := Usage{
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
		TotalTokens:  out.PromptEvalCount + out.EvalCount,
	}
	return out.Message.Content, u, nil
}

// resolveModel returns the model to use: an explicit per-request model wins, else the
// configured default, else a lazily-detected locally-available model (so the leaf works
// zero-config wherever ANY model is pulled). The detected model is cached.
func (c *ollamaClient) resolveModel(ctx context.Context, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if c.model != "" {
		return c.model, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoModel != "" {
		return c.autoModel, nil
	}
	m, err := c.firstAvailableModel(ctx)
	if err != nil {
		return "", err
	}
	c.autoModel = m
	return m, nil
}

// listModels queries /api/tags and returns the locally-pulled model names.
func (c *ollamaClient) listModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags: status %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out, nil
}

// firstAvailableModel returns the first locally-pulled model name (zero-config default).
func (c *ollamaClient) firstAvailableModel(ctx context.Context) (string, error) {
	models, err := c.listModels(ctx)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", errors.New("no ollama models pulled")
	}
	return models[0], nil
}

// OllamaModels lists the names of locally-pulled ollama models, for the dashboard model picker.
func OllamaModels(ctx context.Context, cfg OllamaConfig) ([]string, error) {
	return newOllamaClient(cfg).listModels(ctx)
}

// NewOllama returns the local-ollama leaf processor (Kind "ollama"). lim carries the
// server-side answer-token and path-context guards.
func NewOllama(cfg OllamaConfig, lim Limits) prizm.Processor {
	c := newOllamaClient(cfg)
	return prizm.NewTyped(func(ctx context.Context, in Request, env prizm.Env) (Result, error) {
		if err := validate(in); err != nil {
			return Result{}, err
		}
		model := in.Model
		if model == "" {
			model = c.model
		}
		prompt, items, truncated, err := assemble(ctx, env, in, lim)
		if err != nil {
			return Result{}, err
		}
		content, usage, err := c.chat(ctx, model, defaultSystem, prompt, answerBudget(in, lim.MaxTokens))
		if err != nil {
			return Result{}, err
		}
		usage.Truncated = truncated
		return Result{Output: content, Engine: KindOllama, Model: model, Usage: usage, Context: items}, nil
	})
}
