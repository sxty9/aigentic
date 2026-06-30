package aigentic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sxty9/prizm/prizm"
)

// defaultClaudeModel is the claude-api leaf's default model. Sonnet 4.6 is the chosen
// default (best speed/intelligence balance); override per-request via Request.Model or
// per-deployment via AIGENTIC_CLAUDE_MODEL.
const defaultClaudeModel = "claude-sonnet-4-6"

// ClaudeAPIConfig configures the API-key leaf. BaseURL/Client are injectable for tests.
type ClaudeAPIConfig struct {
	BaseURL string       // default "https://api.anthropic.com"
	APIKey  string       // static key; empty + no KeyFunc => ErrProcessorUnavailable
	Model   string       // default defaultClaudeModel
	Version string       // anthropic-version header; default "2023-06-01"
	Client  *http.Client // default http.DefaultClient

	// KeyFunc, when set, supplies the API key for the REQUESTING user at request time and
	// overrides APIKey when it returns ok. subject is the server-stamped holistic username, so
	// each user is billed to their own key (the store falls back to a shared/global key). Read
	// per request, so a change takes effect without restarting the daemon.
	KeyFunc func(subject string) (string, bool)
}

// NewClaudeAPI returns the Anthropic-API leaf processor (Kind "claude-api"). lim carries
// the server-side answer-token and path-context guards.
func NewClaudeAPI(cfg ClaudeAPIConfig, lim Limits) prizm.Processor {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	version := cfg.Version
	if version == "" {
		version = "2023-06-01"
	}
	defModel := cfg.Model
	if defModel == "" {
		defModel = defaultClaudeModel
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}

	return prizm.NewTyped(func(ctx context.Context, in Request, env prizm.Env) (Result, error) {
		if err := validate(in); err != nil {
			return Result{}, err
		}
		apiKey := cfg.APIKey
		if cfg.KeyFunc != nil {
			if k, ok := cfg.KeyFunc(env.Header.Subject); ok && k != "" {
				apiKey = k
			}
		}
		if apiKey == "" {
			return Result{}, fmt.Errorf("%w: ANTHROPIC_API_KEY unset", ErrProcessorUnavailable)
		}
		model := in.Model
		if model == "" {
			model = defModel
		}
		var effort string
		if in.Claude != nil {
			effort = in.Claude.Effort
		}
		if effort != "" && !validEffort(effort) {
			return Result{}, fmt.Errorf("%w: bad effort %q", prizm.ErrInvalidRequest, effort)
		}
		prompt, items, truncated, err := assemble(ctx, env, in, lim)
		if err != nil {
			return Result{}, err
		}
		body := map[string]any{
			"model":      model,
			"max_tokens": answerBudget(in, lim.MaxTokens),
			"system":     defaultSystem,
			"messages":   []map[string]any{{"role": "user", "content": prompt}},
		}
		if effort != "" {
			// Anthropic carries reasoning effort inside output_config, not top-level.
			body["output_config"] = map[string]any{"effort": effort}
		}
		reqBody, err := json.Marshal(body)
		if err != nil {
			return Result{}, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(reqBody))
		if err != nil {
			return Result{}, err
		}
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", version)

		resp, err := client.Do(httpReq)
		if err != nil {
			return Result{}, fmt.Errorf("%w: anthropic: %v", ErrProcessorUnavailable, err)
		}
		defer resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return Result{}, fmt.Errorf("%w: anthropic 401 (bad API key)", ErrProcessorUnavailable)
		case resp.StatusCode == http.StatusBadRequest:
			return Result{}, fmt.Errorf("%w: anthropic 400", prizm.ErrInvalidRequest)
		case resp.StatusCode != http.StatusOK:
			return Result{}, fmt.Errorf("anthropic: status %d", resp.StatusCode)
		}

		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Result{}, err
		}
		var text string
		for _, c := range out.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		u := Usage{
			InputTokens:  out.Usage.InputTokens,
			OutputTokens: out.Usage.OutputTokens,
			TotalTokens:  out.Usage.InputTokens + out.Usage.OutputTokens,
			Truncated:    truncated,
		}
		return Result{Output: text, Engine: KindClaudeAPI, Model: model, Effort: effort, Usage: u, Context: items}, nil
	})
}
