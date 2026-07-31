// This file is aigentic's MCP face: the same capability the REST /run surface exposes — running an
// AI request through the router — offered as a Model-Context-Protocol tool so an agent can drive
// aigentic directly (MCP axiom). It is deliberately thin: the one tool routes through the P-layer
// aigentic.Ask against the SAME registry /run uses, so nothing here re-implements routing or the
// engines, and the HTTP shell still never decodes Data itself. Every tool re-checks the same rights
// the REST door enforces — every MCP capability is covered by the rights system (MCP axiom).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/auth"
	"github.com/sxty9/aigentic/backend/internal/mcp"
	"github.com/sxty9/aigentic/backend/internal/rights"
	"github.com/sxty9/prizm/prizm"
)

// authenticateMCP resolves the caller from the holistic session cookie — the SAME identity resolver
// the REST guard uses (s.v.User), so the two doors cannot drift on who a caller is. The endpoint is
// therefore addressable via the central infrastructure at the fixed, server-side path base+"mcp"; a
// request never carries the server address (no SSRF). (Minting bearer tokens for external MCP
// clients, as hosuto does, is a future addition; the session door already satisfies the axiom.)
func (s *Server) authenticateMCP(r *http.Request) (any, error) {
	return s.v.User(r)
}

// mcpRegistry builds the tool set once, at wiring time.
func (s *Server) mcpRegistry() *mcp.Registry {
	reg := mcp.NewRegistry(service, version)
	reg.Register(mcp.Tool{
		Name: "aigentic.ask",
		Description: "Ask an AI a question and get its answer. By default the router estimates difficulty and " +
			"picks the engine (a local model for easy asks, Claude for harder ones); set \"engine\" to pin one. " +
			"Returns the answer plus which engine and model actually ran and the token usage. Using the paid " +
			"Claude API requires the aigentic 'cost:api' right.",
		InputSchema: schemaObject(map[string]any{
			"prompt": map[string]any{"type": "string", "description": "The question or instruction to answer."},
			"engine": map[string]any{
				"type":        "string",
				"enum":        []string{"choose", "ollama", "claude-cli", "claude-api"},
				"description": "Which engine to use. Default \"choose\" (auto-route by difficulty).",
			},
			"model":     map[string]any{"type": "string", "description": "Optional model id override (engine-specific)."},
			"maxTokens": map[string]any{"type": "integer", "description": "Optional answer-token ceiling (clamped server-side)."},
		}, "prompt"),
		Handler: s.mcpAsk,
	})
	return reg
}

// mcpAsk runs one aigentic.ask call: it enforces the same rights as REST /run, then routes through
// the P-layer aigentic.Ask against the shared registry. The subject is server-authoritative (the
// resolved holistic identity), never taken from the arguments.
func (s *Server) mcpAsk(ctx context.Context, cAny any, args json.RawMessage) (any, error) {
	u, _ := cAny.(*auth.User)
	if u == nil {
		return nil, errors.New("not authenticated")
	}
	// Base gate — the same right the REST /run route requires.
	if !u.Can(rights.GroupRun) {
		return nil, errors.New("you do not have permission to run aigentic")
	}
	var a struct {
		Prompt    string `json:"prompt"`
		Engine    string `json:"engine"`
		Model     string `json:"model"`
		MaxTokens int    `json:"maxTokens"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	kind := prizm.Kind(a.Engine)
	if kind == "" {
		kind = aigentic.KindChoose
	}
	switch kind {
	case aigentic.KindChoose, aigentic.KindOllama, aigentic.KindClaudeCLI, aigentic.KindClaudeAPI:
	default:
		return nil, errors.New("unknown engine: " + a.Engine)
	}
	// Paid-engine gate — identical to the REST dispatch rule (reads Kind only, never Data).
	if paidKind(kind) && !u.Can(rights.GroupAPI) {
		return nil, errors.New("the paid Claude API requires the aigentic 'cost:api' right")
	}

	res, err := aigentic.Route(ctx, s.reg, kind, u.Username, aigentic.Request{
		Prompt:    a.Prompt,
		Model:     a.Model,
		MaxTokens: a.MaxTokens,
	})
	if err != nil {
		// Surface a readable tool error (isError:true) the model can adapt to.
		switch {
		case errors.Is(err, aigentic.ErrNoVisionEngine):
			return nil, errors.New("the attached image(s) could not be read: no image-capable model is available")
		case errors.Is(err, aigentic.ErrProcessorUnavailable):
			return nil, errors.New("the selected engine is unavailable")
		case errors.Is(err, prizm.ErrInvalidRequest):
			return nil, errors.New("invalid request: " + err.Error())
		default:
			return nil, errors.New("the engine failed to answer")
		}
	}
	return map[string]any{
		"output": res.Output,
		"engine": res.Engine,
		"model":  res.Model,
		"usage":  res.Usage,
	}, nil
}

// schemaObject builds a JSON-Schema object from a property map and an optional required list.
func schemaObject(props map[string]any, required ...string) json.RawMessage {
	if props == nil {
		props = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	b, _ := json.Marshal(m)
	return json.RawMessage(b)
}
