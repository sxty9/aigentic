package aigentic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sxty9/prizm/prizm"
)

// ExecRunner runs a command with stdin + extra env and returns its stdout. Injectable so tests
// can fake the CLI without a real `claude` binary or subscription login. extraEnv carries the
// per-user CLAUDE_CONFIG_DIR / CLAUDE_CODE_OAUTH_TOKEN (appended to the daemon's environment).
type ExecRunner func(ctx context.Context, name string, args []string, stdin string, extraEnv []string, dir string) (stdout []byte, err error)

func defaultExecRunner(ctx context.Context, name string, args []string, stdin string, extraEnv []string, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	if dir != "" {
		cmd.Dir = dir // run in the materialized work dir so the CLI's file tools resolve real paths
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// ClaudeCLIConfig configures the subscription-CLI leaf. The leaf runs `claude` PER USER: each
// request uses the requesting Subject's own subscription token + config dir, so no one bears
// another user's token load. TokenFunc/ConfigDirFunc are injectable (store-backed in prod,
// stubbed in tests).
type ClaudeCLIConfig struct {
	Bin   string     // path to the `claude` binary; default "claude" (resolved via PATH)
	Model string     // optional --model
	Run   ExecRunner // default defaultExecRunner; set in tests to fake the CLI

	// TokenFunc returns the requesting user's linked Claude subscription token (from
	// `claude setup-token`), injected as CLAUDE_CODE_OAUTH_TOKEN (bills their Pro/Max). Preferred
	// over KeyFunc when both are present. nil => subscription billing not offered.
	TokenFunc func(subject string) (string, bool)
	// KeyFunc returns the requesting user's OWN Anthropic API key (never a shared/global fallback),
	// injected as ANTHROPIC_API_KEY when they have no subscription token — so a user who configured an
	// API key in aigentic gets the agentic engine too, billed to their own Console. nil => API-key
	// billing not offered for this leaf.
	//
	// With TokenFunc and/or KeyFunc set, a user with NEITHER credential makes this leaf
	// ErrProcessorUnavailable, so choose falls back and the Ask-AI tab tells them to connect one.
	KeyFunc func(subject string) (string, bool)
	// ConfigDirFunc returns (creating) the user's CLAUDE_CONFIG_DIR so each user's CLI session
	// is isolated. nil => the CLI uses its default (the daemon's HOME).
	ConfigDirFunc func(subject string) (string, error)
	// MCPProviders maps a provider name to its base URL. It is the allow-list of MCP servers a request
	// may attach by name (see MCPRef); the URL is never taken from the wire, so a crafted request
	// cannot aim the CLI at an arbitrary host. Empty => no MCP servers can be attached.
	MCPProviders map[string]string
}

// NewClaudeCLI returns the subscription-CLI leaf processor (Kind "claude-cli"). The CLI
// has no answer-token cap flag, so lim.MaxTokens is not passed to the engine; the budget
// is enforced prompt-side (context assembly) and via post-call accounting only.
func NewClaudeCLI(cfg ClaudeCLIConfig, lim Limits) prizm.Processor {
	bin := cfg.Bin
	if bin == "" {
		bin = "claude"
	}
	run := cfg.Run
	if run == nil {
		run = defaultExecRunner
	}

	return prizm.NewTyped(func(ctx context.Context, in Request, env prizm.Env) (Result, error) {
		if err := validate(in); err != nil {
			return Result{}, err
		}
		// Only the real runner needs a binary on PATH; a fake runner needs none.
		if cfg.Run == nil {
			if _, err := exec.LookPath(bin); err != nil {
				return Result{}, fmt.Errorf("%w: claude CLI not found: %v", ErrProcessorUnavailable, err)
			}
		}
		// Per-user credentials: whatever the requesting user configured in aigentic — a subscription
		// token (billed to their Pro/Max) or their own API key (billed to their Console). Either drives
		// the same agentic run, so the Ask-AI chat works for both, not only subscribers. With gating
		// configured but NEITHER credential present, the leaf is unavailable (choose falls back, and the
		// tab prompts them to connect one).
		subject := env.Header.Subject
		var extraEnv []string
		if cfg.TokenFunc != nil || cfg.KeyFunc != nil {
			// Resolve the credential BEFORE isolating a config dir, so a user with none is turned away
			// without leaving a stray directory behind.
			var credEnv string
			if cfg.TokenFunc != nil {
				if tok, ok := cfg.TokenFunc(subject); ok && tok != "" {
					credEnv = "CLAUDE_CODE_OAUTH_TOKEN=" + tok
				}
			}
			if credEnv == "" && cfg.KeyFunc != nil {
				if key, ok := cfg.KeyFunc(subject); ok && key != "" {
					credEnv = "ANTHROPIC_API_KEY=" + key
				}
			}
			if credEnv == "" {
				return Result{}, fmt.Errorf("%w: no Claude credential for %q — link a subscription or an API key", ErrProcessorUnavailable, subject)
			}
			// Isolate the user's CLI session. Fail CLOSED — NEVER run a user's credential in the shared
			// service-account dir, or concurrent users would share one session/credential cache.
			if cfg.ConfigDirFunc != nil {
				dir, derr := cfg.ConfigDirFunc(subject)
				if derr != nil || dir == "" {
					return Result{}, fmt.Errorf("%w: cannot isolate Claude config dir for %q: %v", ErrProcessorUnavailable, subject, derr)
				}
				extraEnv = append(extraEnv, "CLAUDE_CONFIG_DIR="+dir)
			}
			extraEnv = append(extraEnv, credEnv)
		} else if cfg.ConfigDirFunc != nil {
			// No per-user gating (tests / ambient login): best-effort config dir.
			if dir, derr := cfg.ConfigDirFunc(subject); derr == nil && dir != "" {
				extraEnv = append(extraEnv, "CLAUDE_CONFIG_DIR="+dir)
			}
		}
		model := in.Model
		if model == "" {
			model = cfg.Model
		}
		args := []string{"-p", "--output-format", "json"}
		if model != "" {
			args = append(args, "--model", model)
		}
		// Reasoning effort: the CLI takes --effort (low|medium|high|xhigh|max), same set as the
		// API leaf. Validate up front so a bad level is a 400, not an opaque CLI failure.
		var effort string
		if in.Claude != nil {
			effort = in.Claude.Effort
		}
		if effort != "" {
			if !validEffort(effort) {
				return Result{}, fmt.Errorf("%w: bad effort %q", prizm.ErrInvalidRequest, effort)
			}
			args = append(args, "--effort", effort)
		}
		// Claude Code is agentic: it reads the REAL filesystem, not our virtual Samba paths. So
		// instead of naming/embedding files in the prompt (which made it try to open a path that
		// "doesn't exist on disk"), materialize the attachments to a private temp dir and run the
		// CLI THERE — its Read tool then opens them for real (images via vision included).
		workdir, listing, items, merr := materializeCLIFiles(in)
		if merr != nil {
			return Result{}, fmt.Errorf("%w: claude-cli workdir: %v", ErrProcessorUnavailable, merr)
		}
		var allowed []string
		if workdir != "" {
			defer os.RemoveAll(workdir)
			// Read-only tools only: the CLI may open/inspect the files, nothing else (no Bash/Write).
			allowed = append(allowed, "Read", "Glob", "Grep")
		}
		// Attach MCP servers → the CLI becomes agentic against them. This is how the Ask-AI tab binds a
		// chat to hosuto's tools: each ref names a daemon-configured provider (URL is server-side, so no
		// SSRF from the wire) and carries the user's own scoped token. --strict-mcp-config ignores any
		// ambient config; the mcp__<name>__* allow-list pre-approves exactly those tools in headless -p.
		if len(in.MCP) > 0 {
			mcpFile, names, mcperr := writeMCPConfig(cfg.MCPProviders, in.MCP)
			if mcperr != nil {
				return Result{}, fmt.Errorf("%w: mcp config: %v", prizm.ErrInvalidRequest, mcperr)
			}
			defer os.Remove(mcpFile)
			args = append(args, "--mcp-config", mcpFile, "--strict-mcp-config")
			for _, n := range names {
				allowed = append(allowed, "mcp__"+n+"__*")
			}
		}
		if len(allowed) > 0 {
			args = append(args, "--allowedTools")
			args = append(args, allowed...)
		}
		// Server-bound guidance goes to the SYSTEM prompt (not the user turn), so it shapes every step
		// of the agentic loop without being echoed back as if the user had said it.
		if sys := strings.TrimSpace(in.System); sys != "" {
			args = append(args, "--append-system-prompt", sys)
		}
		prompt := composeCLIPrompt(substrateGuidance(lim, env.Grave), listing, in)
		// Prompt goes on stdin to avoid ARG_MAX with large context.
		stdout, err := run(ctx, bin, args, prompt, extraEnv, workdir)
		if err != nil {
			// A non-zero exit (not logged in, bad flags) reads as unavailability here.
			return Result{}, fmt.Errorf("%w: %v", ErrProcessorUnavailable, err)
		}

		var out struct {
			Type      string  `json:"type"`
			Result    string  `json:"result"`
			IsError   bool    `json:"is_error"`
			TotalCost float64 `json:"total_cost_usd"`
			Usage     struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(stdout, &out); err != nil {
			return Result{}, fmt.Errorf("claude-cli: bad json output: %v", err)
		}
		if out.IsError {
			return Result{}, fmt.Errorf("claude-cli: %s", out.Result)
		}
		u := Usage{
			InputTokens:  out.Usage.InputTokens,
			OutputTokens: out.Usage.OutputTokens,
			TotalTokens:  out.Usage.InputTokens + out.Usage.OutputTokens,
		}
		return Result{Output: out.Result, Engine: KindClaudeCLI, Model: model, Effort: effort, Usage: u, Context: items}, nil
	})
}

// materializeCLIFiles writes the request's inline attachments into a fresh private temp dir so
// the agentic CLI can open them as real files. Returns the dir (caller removes it), a human file
// listing for the prompt, and provenance items. No inline files → ("", "", nil, nil), so a plain
// chat turn runs without a work dir.
func materializeCLIFiles(in Request) (dir, listing string, items []ContextItem, err error) {
	if len(in.Inline) == 0 {
		return "", "", nil, nil
	}
	dir, err = os.MkdirTemp("", "aigentic-cli-")
	if err != nil {
		return "", "", nil, err
	}
	var b strings.Builder
	seen := map[string]int{}
	for _, f := range in.Inline {
		name := safeName(f.Path, seen)
		var data []byte
		if f.isText() {
			data = []byte(f.Content)
		} else {
			// image/pdf/other rides as base64; decode back to real bytes on disk.
			d, derr := base64.StdEncoding.DecodeString(f.Content)
			if derr != nil {
				items = append(items, ContextItem{Path: f.Path, Skipped: "attachment"})
				continue
			}
			data = d
		}
		if len(data) == 0 {
			// A name-only entry (e.g. an unreadable "other" file type) — nothing to write.
			items = append(items, ContextItem{Path: f.Path, Skipped: "empty"})
			continue
		}
		if werr := os.WriteFile(filepath.Join(dir, name), data, 0o600); werr != nil {
			items = append(items, ContextItem{Path: f.Path, Skipped: "denied"})
			continue
		}
		fmt.Fprintf(&b, "- %s\n", name)
		items = append(items, ContextItem{Path: f.Path, Bytes: len(data)})
	}
	return dir, b.String(), items, nil
}

// safeName reduces a (possibly nested, possibly hostile) virtual path to a single safe filename
// for the flat work dir, de-duplicating collisions.
func safeName(p string, seen map[string]int) string {
	name := filepath.Base(filepath.Clean("/" + p)) // strips dirs and any ".." traversal
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "file"
	}
	if n := seen[name]; n > 0 {
		seen[name] = n + 1
		ext := filepath.Ext(name)
		return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
	}
	seen[name] = 1
	return name
}

// writeMCPConfig writes a temporary .mcp.json for the requested servers and returns its path and the
// server names to allow-list. Only refs whose Name is a configured provider are included: the daemon
// holds the URL, so the wire can never point the CLI at an arbitrary host (no SSRF from a crafted
// request). An unknown provider is an error, not a silent drop, so a misconfiguration is visible
// rather than a chat that quietly has no tools. The file is 0600 and the caller removes it.
func writeMCPConfig(providers map[string]string, refs []MCPRef) (path string, names []string, err error) {
	servers := map[string]any{}
	for _, ref := range refs {
		url := providers[ref.Name]
		if url == "" {
			return "", nil, fmt.Errorf("unknown MCP provider %q", ref.Name)
		}
		entry := map[string]any{"type": "http", "url": url}
		if ref.Token != "" {
			entry["headers"] = map[string]any{"Authorization": "Bearer " + ref.Token}
		}
		servers[ref.Name] = entry
		names = append(names, ref.Name)
	}
	b, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "aigentic-mcp-*.json")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), names, nil
}

// composeCLIPrompt points the CLI at the materialized files (if any), then the
// instruction — with an optional substrate-guidance preamble (from a Describer
// graveyard, e.g. scheme) so the claude-cli leaf gets the same structure guidance
// as the ollama/claude-api leaves (which inject it via composePrompt).
func composeCLIPrompt(guidance, listing string, in Request) string {
	var b strings.Builder
	if guidance != "" {
		b.WriteString(guidance)
		b.WriteString("\n\n")
	}
	if listing != "" {
		b.WriteString("The following files are in your current working directory — read them as needed to answer:\n")
		b.WriteString(listing)
		b.WriteString("\n")
	}
	b.WriteString(in.Prompt)
	if in.OutputFormat != "" && in.OutputFormat != "text" {
		fmt.Fprintf(&b, "\n\nRespond in %s format.", in.OutputFormat)
	}
	return b.String()
}
