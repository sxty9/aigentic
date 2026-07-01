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
	// `claude setup-token`). When set and it returns no token, the leaf reports
	// ErrProcessorUnavailable (the user hasn't linked their Claude) so choose falls back.
	// The token is injected as CLAUDE_CODE_OAUTH_TOKEN. nil => no per-user gating (tests).
	TokenFunc func(subject string) (string, bool)
	// ConfigDirFunc returns (creating) the user's CLAUDE_CONFIG_DIR so each user's CLI session
	// is isolated. nil => the CLI uses its default (the daemon's HOME).
	ConfigDirFunc func(subject string) (string, error)
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
		// Per-user credentials: the requesting user's subscription token + config dir. With
		// TokenFunc set, a user who hasn't linked their Claude makes this leaf unavailable, so
		// choose falls back to another engine (e.g. ollama) rather than failing the request.
		subject := env.Header.Subject
		var extraEnv []string
		if cfg.TokenFunc != nil {
			// Per-user subscription: an unlinked user makes this leaf unavailable (choose falls
			// back), and the token bills their own Claude.
			tok, ok := cfg.TokenFunc(subject)
			if !ok || tok == "" {
				return Result{}, fmt.Errorf("%w: no Claude subscription linked for %q", ErrProcessorUnavailable, subject)
			}
			// Isolate the user's CLI session in their own CLAUDE_CONFIG_DIR. Fail CLOSED if it
			// can't be set up — NEVER run a user's token in the shared service-account dir, or
			// concurrent users would share one session/credential cache.
			if cfg.ConfigDirFunc != nil {
				dir, derr := cfg.ConfigDirFunc(subject)
				if derr != nil || dir == "" {
					return Result{}, fmt.Errorf("%w: cannot isolate Claude config dir for %q: %v", ErrProcessorUnavailable, subject, derr)
				}
				extraEnv = append(extraEnv, "CLAUDE_CONFIG_DIR="+dir)
			}
			extraEnv = append(extraEnv, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
		} else if cfg.ConfigDirFunc != nil {
			// No per-user token gating (tests / ambient login): best-effort config dir.
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
		if workdir != "" {
			defer os.RemoveAll(workdir)
			// Read-only tools only: the CLI may open/inspect the files, nothing else (no Bash/Write).
			args = append(args, "--allowedTools", "Read", "Glob", "Grep")
		}
		prompt := composeCLIPrompt(listing, in)
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

// composeCLIPrompt points the CLI at the materialized files (if any), then the instruction.
func composeCLIPrompt(listing string, in Request) string {
	var b strings.Builder
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
