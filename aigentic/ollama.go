package aigentic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sxty9/prizm/prizm"
)

// OllamaConfig configures the local-ollama leaf. BaseURL/Client are injectable so tests
// can point at an httptest server — no real ollama daemon required.
type OllamaConfig struct {
	BaseURL string       // default "http://localhost:11434"
	Model   string       // default model when Request.Model is empty
	Client  *http.Client // default http.DefaultClient
}

// ollamaClient is the minimal /api/chat client, shared by the ollama leaf and the
// choose router's classifier (so the cheap classification call reuses one code path).
type ollamaClient struct {
	base   string
	model  string
	client *http.Client
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
	return &ollamaClient{base: base, model: cfg.Model, client: client}
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
	if model == "" {
		model = c.model
	}
	msgs := make([]map[string]string, 0, 2)
	if system != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": user})

	options := map[string]any{"num_predict": numPredict}
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
		return "", Usage{}, fmt.Errorf("%w: ollama model %q not found (pull it first)", prizm.ErrInvalidRequest, model)
	case resp.StatusCode != http.StatusOK:
		return "", Usage{}, fmt.Errorf("ollama: status %d", resp.StatusCode)
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
