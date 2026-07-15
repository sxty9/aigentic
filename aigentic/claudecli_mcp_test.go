package aigentic

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sxty9/prizm/prizm"
)

func okCLI(capArgs *[]string, capEnv *[]string, capMCP *string) ExecRunner {
	return func(_ context.Context, _ string, args []string, _ string, extraEnv []string, _ string) ([]byte, error) {
		if capArgs != nil {
			*capArgs = args
		}
		if capEnv != nil {
			*capEnv = extraEnv
		}
		if capMCP != nil {
			for i, a := range args {
				if a == "--mcp-config" && i+1 < len(args) {
					if b, err := os.ReadFile(args[i+1]); err == nil {
						*capMCP = string(b)
					}
				}
			}
		}
		return json.Marshal(map[string]any{
			"type": "result", "result": "ok", "is_error": false,
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}
}

// Attaching an MCP provider makes the CLI agentic: the temp .mcp.json carries the daemon-configured
// URL + the caller's bearer token, --strict-mcp-config is set, the provider's tools are allow-listed,
// and server-bound guidance rides on --append-system-prompt (not the user turn).
func TestClaudeCLIMCP(t *testing.T) {
	var args []string
	var mcpJSON string
	providers := map[string]string{"hosuto": "http://127.0.0.1:8779/api/services/hosuto/mcp"}
	reg := newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: okCLI(&args, nil, &mcpJSON), MCPProviders: providers}})

	in := Request{
		Prompt: "start my server",
		System: "You operate the Minecraft server smp.",
		MCP:    []MCPRef{{Name: "hosuto", Token: "hmcp_secret"}},
	}
	if out := mustRoute(t, reg, KindClaudeCLI, in); out.Output != "ok" {
		t.Fatalf("output=%q", out.Output)
	}
	if !envHas(args, "--strict-mcp-config") {
		t.Fatalf("--strict-mcp-config missing: %v", args)
	}
	if !argPair(args, "--allowedTools", "mcp__hosuto__*") {
		t.Fatalf("hosuto tools not allow-listed: %v", args)
	}
	if !argPair(args, "--append-system-prompt", "You operate the Minecraft server smp.") {
		t.Fatalf("system prompt not appended: %v", args)
	}
	if !strings.Contains(mcpJSON, "127.0.0.1:8779") || !strings.Contains(mcpJSON, "Bearer hmcp_secret") {
		t.Fatalf("mcp config missing url/token: %s", mcpJSON)
	}

	// An unknown provider is a visible error (400), never a silent tool-less chat, and never an
	// attacker-chosen URL.
	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: okCLI(nil, nil, nil), MCPProviders: providers}})
	if _, err := route(reg, KindClaudeCLI, Request{Prompt: "x", MCP: []MCPRef{{Name: "evil"}}}); !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("unknown provider: err=%v want ErrInvalidRequest", err)
	}
}

// The agentic leaf honours whatever billing a user configured: a subscription token is preferred,
// otherwise their own API key bills their Console, and with neither the leaf is unavailable.
func TestClaudeCLICredentialSelection(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	key := func(string) (string, bool) { return "sk-ant-api-userkey", true }
	tok := func(string) (string, bool) { return "claude_token", true }
	cfgOK := func(string) (string, error) { return t.TempDir(), nil }

	// No token, but an API key → ANTHROPIC_API_KEY, and no OAuth token leaks in.
	var env []string
	reg := newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: okCLI(nil, &env, nil), TokenFunc: none, KeyFunc: key, ConfigDirFunc: cfgOK}})
	mustRoute(t, reg, KindClaudeCLI, Request{Prompt: "hi"})
	if !envHas(env, "ANTHROPIC_API_KEY=sk-ant-api-userkey") {
		t.Fatalf("api-key user not billed via ANTHROPIC_API_KEY: %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("oauth token leaked for an api-key user: %v", env)
		}
	}

	// A subscription token wins over an API key.
	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: okCLI(nil, &env, nil), TokenFunc: tok, KeyFunc: key, ConfigDirFunc: cfgOK}})
	mustRoute(t, reg, KindClaudeCLI, Request{Prompt: "hi"})
	if !envHas(env, "CLAUDE_CODE_OAUTH_TOKEN=claude_token") {
		t.Fatalf("subscription token not preferred: %v", env)
	}

	// Neither credential → unavailable (choose falls back; the tab prompts to connect one).
	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: okCLI(nil, nil, nil), TokenFunc: none, KeyFunc: none, ConfigDirFunc: cfgOK}})
	if _, err := route(reg, KindClaudeCLI, Request{Prompt: "hi"}); !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("no credential: err=%v want ErrProcessorUnavailable", err)
	}
}
