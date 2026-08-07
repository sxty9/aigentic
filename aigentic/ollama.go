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
	autoModel string          // lazily-detected model when none is configured (zero-config)
	vision    map[string]bool // per-model vision capability, cached (a model's caps don't change)
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

// chat issues a non-streaming /api/chat call and returns the assistant content + usage. images
// (base64, no data-URI prefix) ride on the user message for a vision model; nil for a text turn.
func (c *ollamaClient) chat(ctx context.Context, model, system, user string, numPredict int, images []string) (string, Usage, error) {
	return c.chatFormat(ctx, model, system, user, numPredict, nil, images)
}

// chatFormat is chat with an optional ollama structured-output schema. When format is
// non-nil it constrains the model to emit JSON matching that schema (so even a tiny model
// follows the shape) and pins temperature to 0 for a deterministic estimate; the plain
// leaf path passes nil and stays free-form. images (base64) are attached to the user message
// for a vision model — the classifier path passes nil.
func (c *ollamaClient) chatFormat(ctx context.Context, model, system, user string, numPredict int, format any, images []string) (string, Usage, error) {
	resolved, err := c.resolveModel(ctx, model)
	if err != nil {
		// No model to run (none configured and none pulled) is unavailability, not a hard
		// failure — so the choose router falls back to another engine.
		return "", Usage{}, fmt.Errorf("%w: ollama: %v", ErrProcessorUnavailable, err)
	}
	model = resolved
	msgs := make([]map[string]any, 0, 2)
	if system != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": system})
	}
	userMsg := map[string]any{"role": "user", "content": user}
	if len(images) > 0 {
		userMsg["images"] = images
	}
	msgs = append(msgs, userMsg)

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

// supportsVision reports whether the resolved ollama model can see images. It is answered from the
// model's OWN advertised capabilities (ollama's /api/show returns a "capabilities" list that names
// "vision" for a multimodal model) — a PROPERTY of the running machine, never a hardcoded list of
// model names that goes stale at the next release. The result is cached (a model's capabilities do
// not change). A probe failure is returned as an error so the caller can refuse rather than guess.
func (c *ollamaClient) supportsVision(ctx context.Context, model string) (bool, error) {
	c.mu.Lock()
	if v, ok := c.vision[model]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	caps, err := c.showCapabilities(ctx, model)
	if err != nil {
		return false, err
	}
	seesImages := false
	for _, cap := range caps {
		if cap == "vision" {
			seesImages = true
			break
		}
	}
	c.mu.Lock()
	if c.vision == nil {
		c.vision = map[string]bool{}
	}
	c.vision[model] = seesImages
	c.mu.Unlock()
	return seesImages, nil
}

// showCapabilities queries /api/show for a model's advertised capabilities (e.g. "completion",
// "tools", "vision"). This is ollama's own report of what the model can do; the leaf reads it rather
// than assuming, so a local vision model qualifies and a text model is refused, both by fact.
func (c *ollamaClient) showCapabilities(ctx context.Context, model string) ([]string, error) {
	body, err := json.Marshal(map[string]any{"model": model})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ollama /api/show: %v", ErrProcessorUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama /api/show: status %d", ErrProcessorUnavailable, resp.StatusCode)
	}
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Capabilities, nil
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

// LoadedModel is one model ollama currently has resident (from /api/ps), enriched for the
// dashboard's residency readout. It answers, without guessing, "what is loaded, how big, fully on
// the GPU(s) or spilled to CPU, with which context window, and when does keep-alive unload it" —
// exactly the transparency for why the SSD spins up (a cold load) and how long a model stays hot.
type LoadedModel struct {
	Name          string `json:"name"`          // e.g. "qwen2.5:14b"
	SizeBytes     int64  `json:"sizeBytes"`     // total resident size (VRAM + any CPU-offloaded layers)
	VRAMBytes     int64  `json:"vramBytes"`     // bytes actually on the GPU(s)
	FullyOnGPU    bool   `json:"fullyOnGpu"`    // vramBytes covers the whole model (no CPU spill)
	ContextLength int    `json:"contextLength"` // num_ctx the resident instance was loaded with (a change forces a reload)
	ExpiresAt     string `json:"expiresAt"`     // RFC3339 keep-alive unload time ("" if ollama sent none)
	ExpiresInSec  int    `json:"expiresInSec"`  // seconds until keep-alive unload, at response time; <=0 => imminent
}

// ps reads /api/ps — the models ollama currently holds in VRAM/RAM, with their keep-alive expiry
// and the context window each was loaded with. ollama owns this truth; we only mirror and enrich it
// (no evaluation of our own). now is injected so tests can assert ExpiresInSec deterministically.
func (c *ollamaClient) ps(ctx context.Context, now time.Time) ([]LoadedModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// A dial error (no ollama running) is unavailability, not a bad request — same mapping as
		// the chat path, so the HTTP shell can surface it as 503 / an empty readout.
		return nil, fmt.Errorf("%w: ollama: %v", ErrProcessorUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama /api/ps: status %d", ErrProcessorUnavailable, resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name          string    `json:"name"`
			Size          int64     `json:"size"`
			SizeVRAM      int64     `json:"size_vram"`
			ContextLength int       `json:"context_length"`
			ExpiresAt     time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]LoadedModel, 0, len(out.Models))
	for _, m := range out.Models {
		lm := LoadedModel{
			Name:          m.Name,
			SizeBytes:     m.Size,
			VRAMBytes:     m.SizeVRAM,
			FullyOnGPU:    m.Size > 0 && m.SizeVRAM >= m.Size,
			ContextLength: m.ContextLength,
		}
		if !m.ExpiresAt.IsZero() {
			lm.ExpiresAt = m.ExpiresAt.Format(time.RFC3339)
			lm.ExpiresInSec = int(m.ExpiresAt.Sub(now).Seconds())
		}
		models = append(models, lm)
	}
	return models, nil
}

// OllamaStatus reports the models ollama currently has resident (from /api/ps), enriched with the
// keep-alive time remaining and the loaded context window. Source for the dashboard's local-model
// residency readout; best-effort — the caller treats any error as "nothing resident".
func OllamaStatus(ctx context.Context, cfg OllamaConfig) ([]LoadedModel, error) {
	return newOllamaClient(cfg).ps(ctx, time.Now())
}

// imageData returns the base64 image payloads from a request's inline attachments, in order, for
// ollama's /api/chat "images" field. Empty content is skipped (nothing to deliver).
// imageData collects the base64 image payloads to hand ollama's vision model, drawn from the
// resolved map assemble produced (so a Ref-only image contributes its stored bytes, not an empty
// string). An image whose resolved payload is empty is dropped rather than delivered blank.
func imageData(in Request, resolved map[string]string) []string {
	var out []string
	for _, f := range in.Inline {
		if f.isImage() {
			if b := resolved[f.Path]; b != "" {
				out = append(out, b)
			}
		}
	}
	return out
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
		// Images decide the engine: a text-only local model must NEVER be handed an image request,
		// or it answers as if it had seen pictures it never received. Resolve the concrete model and
		// consult its OWN advertised capabilities; refuse with a named state when it cannot see images
		// (the router already keeps blind models off image requests — this guards the forced/direct
		// path that reaches the leaf without the router). A vision model is fed the image bytes.
		prompt, items, truncated, resolvedInline, err := assemble(ctx, env, in, lim)
		if err != nil {
			return Result{}, err
		}
		var images []string
		if in.hasImages() {
			resolved, rerr := c.resolveModel(ctx, model)
			if rerr != nil {
				return Result{}, fmt.Errorf("%w: ollama: %v", ErrProcessorUnavailable, rerr)
			}
			ok, verr := c.supportsVision(ctx, resolved)
			if verr != nil {
				return Result{}, fmt.Errorf("%w: cannot determine whether ollama model %q sees images: %v", ErrNoVisionEngine, resolved, verr)
			}
			if !ok {
				return Result{}, fmt.Errorf("%w: ollama model %q has no vision capability", ErrNoVisionEngine, resolved)
			}
			// Deliver the resolved base64 (fresh Content or graveyard bytes for a Ref-only image),
			// never InlineFile.Content raw — a Ref-only image would otherwise reach the vision model
			// as no image at all, and it would answer about a picture it never saw.
			images = imageData(in, resolvedInline)
		}
		content, usage, err := c.chat(ctx, model, askSystem(defaultSystem, in), prompt, answerBudget(in, lim.MaxTokens), images)
		if err != nil {
			return Result{}, err
		}
		usage.Truncated = truncated
		// The local engine reads images only via a vision model (delivered above); it never reads
		// PDFs, so a PDF attachment is named as unread in the answer.
		return finalize(Result{Engine: KindOllama, Model: model, Usage: usage, Context: items}, content, in, true, false), nil
	})
}
