package aigentic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sxty9/prizm/prizm"
)

// ExecRunner runs a command with stdin and returns its stdout. Injectable so tests can
// fake the CLI without a real `claude` binary or subscription login.
type ExecRunner func(ctx context.Context, name string, args []string, stdin string) (stdout []byte, err error)

func defaultExecRunner(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// ClaudeCLIConfig configures the subscription-CLI leaf.
type ClaudeCLIConfig struct {
	Bin   string     // path to the `claude` binary; default "claude" (resolved via PATH)
	Model string     // optional --model
	Run   ExecRunner // default defaultExecRunner; set in tests to fake the CLI
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
		model := in.Model
		if model == "" {
			model = cfg.Model
		}
		args := []string{"-p", "--output-format", "json"}
		if model != "" {
			args = append(args, "--model", model)
		}
		prompt, items, truncated, err := assemble(ctx, env, in, lim)
		if err != nil {
			return Result{}, err
		}
		// Prompt goes on stdin to avoid ARG_MAX with large context.
		stdout, err := run(ctx, bin, args, prompt)
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
			Truncated:    truncated,
		}
		return Result{Output: out.Result, Engine: KindClaudeCLI, Model: model, Usage: u, Context: items}, nil
	})
}
