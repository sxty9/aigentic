// Package aigentic is the first derived prizm (see github.com/sxty9/prizm). It declares
// four processors — three LLM "leaves" (ollama, claude-cli, claude-api) and a "choose"
// router that estimates request complexity and delegates to one of them.
//
// All four share ONE request/result schema — the consolidated derived header (Header₁)
// carried inside prizm's opaque Data₀ — so the router can forward a request VERBATIM to
// any leaf (subprizm.SpawnTyped instantiates In = aigentic.Request for every kind).
//
// The three legs of the triple:
//   - P (this package): the four processors + the one shared header.
//   - G (the graveyard): server-local Paths are read, stored by content-Ref for
//     provenance, and assembled into a bounded context block (see context.go). The
//     backend is any graveyard.Graveyard — the in-memory stub, or lakearch (cgo).
//   - R (the holistic shell, backend/): authenticates the shared session, gates on
//     rights, and routes on Header.Kind into the registry.
//
// What the base already provides — routing (Header.Kind), correlation (Header.ID),
// identity (Header.Subject), the recursion/depth guard (Header.Trace + the registry
// ceiling), the envelope codec (Header.Format), fan-out (subprizm) and large-payload
// storage (the graveyard) — this header deliberately does NOT duplicate.
package aigentic

import (
	"fmt"
	"strings"

	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// The kinds the four processors register under. prizm dispatches on Header.Kind alone.
const (
	KindOllama    prizm.Kind = "ollama"
	KindClaudeCLI prizm.Kind = "claude-cli"
	KindClaudeAPI prizm.Kind = "claude-api"
	KindChoose    prizm.Kind = "choose"
)

// ErrProcessorUnavailable means the engine a processor fronts is not reachable in this
// environment (ollama not installed, ANTHROPIC_API_KEY unset, the CLI not logged in). It
// is distinct from prizm.ErrInvalidRequest (a malformed request): the HTTP shell maps it
// to 503 Service Unavailable, whereas an invalid request maps to 400.
var ErrProcessorUnavailable = fmt.Errorf("aigentic: processor unavailable")

const (
	// DefaultMaxTokens is the answer-token budget used when a request sets none.
	DefaultMaxTokens = 4096
	// DefaultMaxContextBytes bounds the total bytes of file context assembled from Paths.
	DefaultMaxContextBytes = 64 << 10 // 64 KiB
	// maxFileBytes is the per-file size cap when walking Paths.
	maxFileBytes = 256 << 10 // 256 KiB
	// DefaultContextRoot is the allowlisted root under which Paths are confined; the actual
	// scope is <root>/<Subject> so callers cannot read each other's files.
	DefaultContextRoot = "/var/lib/aigentic/context"
)

// defaultSystem is the shared system preamble. Kept tiny.
const defaultSystem = "You are aigentic, a precise assistant. Follow the user's instruction exactly."

// Limits are the server-authoritative ceilings a processor enforces (the token-overusage
// guard and the path-context bounds). Built once by Register from Config/env; never from
// the wire.
type Limits struct {
	MaxTokens       int    // answer-token ceiling; 0 => DefaultMaxTokens
	MaxContextBytes int    // total Paths context budget; 0 => DefaultMaxContextBytes
	ContextRoot     string // allowlisted root for Paths; "" => DefaultContextRoot
	// StoreMode treats the graveyard as an owned data store rather than a provenance sink:
	// per-run context files are NOT written into it, and the substrate-guidance preamble is NOT
	// injected into prompts. Set when the graveyard belongs to a specific application (e.g.
	// Mercury's scheme-backed axiom store) that must not be polluted by, or leak its Leitfaden
	// into, unrelated Ask-AI traffic. Off by default: the graveyard stays a provenance sink and
	// behaviour is byte-identical to before.
	StoreMode bool
}

// Request is THE single aigentic header (Header₁), shared by all four processors and
// carried inside prizm's opaque Data₀.
type Request struct {
	Prompt       string         `json:"prompt"`                 // textual instruction; required
	Paths        []string       `json:"paths,omitempty"`        // server-local files/folders, confined under <ContextRoot>/<Subject>
	OutputFormat string         `json:"outputFormat,omitempty"` // model answer shape: "text" | "markdown" | "json"
	Model        string         `json:"model,omitempty"`        // optional model override; "" => the processor's default. The model id already carries the model version (e.g. claude-sonnet-4-6, or a dated snapshot), so there is no separate version field.
	MaxTokens    int            `json:"maxTokens,omitempty"`    // token-overusage guard; 0 => DefaultMaxTokens; clamped to the ceiling
	Claude       *ClaudeOptions `json:"claude,omitempty"`       // Claude-leaf knobs (effort, …); nil/ignored for ollama
	Choose       *ChooseOptions `json:"choose,omitempty"`       // router-only knobs; nil/ignored for the three leaves
	Inline       []InlineFile   `json:"inline,omitempty"`       // caller-supplied file contents used as context WITHOUT server fs access (e.g. the Files app reads the user's private share and passes the bytes here); same byte budget + binary filter as Paths
	System       string         `json:"system,omitempty"`       // extra guidance appended to the engine's SYSTEM prompt, so a caller can bind a chat to a domain (e.g. one hosuto server) without putting the context in the user turn. claude-cli only today (--append-system-prompt).
	MCP          []MCPRef       `json:"mcp,omitempty"`          // MCP servers to attach for THIS run, turning the engine agentic against them. claude-cli only (its `claude` binary is a native MCP client).
}

// MCPRef attaches one Model-Context-Protocol server to an agentic run. Name selects a provider the
// daemon has been configured to allow — the URL lives server-side, so a crafted request can never
// point the engine at an arbitrary host (no SSRF from the wire). Token is the caller's own bearer
// credential for that provider, minted by the provider and scoped to what the caller may do there.
// Honoured only by the claude-cli leaf.
type MCPRef struct {
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

// InlineFile is a file's content supplied directly in the request (not read from disk by the
// daemon). It lets a privileged caller — e.g. the holistic Files app, which already has the
// user's confined fs access — hand aigentic the bytes, keeping the daemon unprivileged and
// fs-free for the private Samba share.
type InlineFile struct {
	Path      string `json:"path"`                // display/provenance path (e.g. "me/Notes/spec.md"); never used for fs access
	Content   string `json:"content"`             // text content (mediaType empty/text), else base64-encoded bytes
	MediaType string `json:"mediaType,omitempty"` // "" or "text/*" => text; "image/png|jpeg|gif|webp" => vision; "application/pdf" => document; anything else => listed as an attachment only (counted, not read)
}

// isText reports whether an inline file carries plain text (vs. base64 media).
func (f InlineFile) isText() bool {
	return f.MediaType == "" || strings.HasPrefix(f.MediaType, "text/")
}

// ClaudeOptions are the knobs specific to the Claude leaves (claude-api, claude-cli).
// Nested so the one header stays a single type: ollama never reads them, and on an
// ollama call they need not appear on the wire. Extension point for future Claude-only
// parameters. Today only the claude-api leaf acts on Effort (as output_config.effort);
// claude-cli has no stable per-call effort flag, so it carries but does not translate it.
type ClaudeOptions struct {
	Effort string `json:"effort,omitempty"` // reasoning effort: "low" | "medium" | "high" | "xhigh" | "max"; "" => the model's default
}

// ChooseOptions are the router-only fields. Nested so the schema stays a single type: on
// a leaf call they never appear on the wire and the leaf never reads them.
type ChooseOptions struct {
	Force      prizm.Kind   `json:"force,omitempty"`      // pin a leaf: "ollama" | "claude-cli" | "claude-api"; "" => estimate
	Policy     *RoutePolicy `json:"policy,omitempty"`     // override the complexity→kind mapping
	Classifier string       `json:"classifier,omitempty"` // ollama model used for the cheap classification call
}

// RoutePolicy maps an estimated complexity bucket to a leaf kind.
type RoutePolicy struct {
	Low    prizm.Kind `json:"low,omitempty"`    // default KindOllama
	Medium prizm.Kind `json:"medium,omitempty"` // default KindClaudeCLI
	High   prizm.Kind `json:"high,omitempty"`   // default KindClaudeAPI
}

// Result is the single shared response schema for all four processors.
type Result struct {
	Output   string        `json:"output"`             // the model's answer
	Engine   prizm.Kind    `json:"engine,omitempty"`   // leaf kind that actually ran (for choose: the picked leaf)
	Model    string        `json:"model,omitempty"`    // concrete model id used
	Effort   string        `json:"effort,omitempty"`   // reasoning effort actually applied (claude-api only)
	Usage    Usage         `json:"usage,omitempty"`    // token accounting (the guard's post-call bookkeeping)
	Context  []ContextItem `json:"context,omitempty"`  // provenance of the Paths fed to the model
	Decision *Decision     `json:"decision,omitempty"` // set ONLY by the choose router
}

// Usage is the token accounting reported back by an engine.
type Usage struct {
	InputTokens  int  `json:"inputTokens,omitempty"`
	OutputTokens int  `json:"outputTokens,omitempty"`
	TotalTokens  int  `json:"totalTokens,omitempty"`
	Truncated    bool `json:"truncated,omitempty"` // Paths context was trimmed to fit the budget
}

// ContextItem records one path that the graveyard assembly considered, for provenance:
// the exact bytes fed to the model are reproducible from Ref.
type ContextItem struct {
	Path    string        `json:"path"`
	Ref     graveyard.Ref `json:"ref,omitempty"`     // content-addressed reference to the stored bytes
	Bytes   int           `json:"bytes,omitempty"`   // bytes included
	Skipped string        `json:"skipped,omitempty"` // "missing" | "denied" | "binary" | "too-large" | "budget"
}

// Decision records how the router chose a leaf.
type Decision struct {
	Picked     prizm.Kind `json:"picked"`               // the leaf that ran
	Complexity string     `json:"complexity,omitempty"` // low | medium | high (empty when forced)
	Reason     string     `json:"reason,omitempty"`
	Source     string     `json:"source"`             // "ollama-classifier" | "heuristic" | "forced"
	Fallback   bool       `json:"fallback,omitempty"` // an availability fallback was used
	Spilled    bool       `json:"spilled,omitempty"`  // cli was the healthy pick but api was used to spare subscription headroom
	CLIUsage   float64    `json:"cliUsage,omitempty"` // measured rolling-window subscription utilization (0..1) at decision time
}

// validate enforces the one hard precondition shared by every processor.
func validate(in Request) error {
	if in.Prompt == "" {
		return fmt.Errorf("%w: empty prompt", prizm.ErrInvalidRequest)
	}
	return nil
}

// validEffort reports whether s is a recognized Claude reasoning-effort level. The set is
// model-agnostic (the documented superset); a level the chosen model rejects — e.g. "xhigh"
// on Sonnet — is caught by the API as a 400, which the leaf maps to ErrInvalidRequest.
func validEffort(s string) bool {
	switch s {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// answerBudget resolves the effective answer-token budget: the request's MaxTokens,
// defaulted and clamped to the ceiling. This is the per-request half of the
// token-overusage guard (the ceiling is the server-authoritative half, analogous to the
// registry's recursion ceiling).
func answerBudget(in Request, ceiling int) int {
	if ceiling <= 0 {
		ceiling = DefaultMaxTokens
	}
	b := in.MaxTokens
	if b <= 0 {
		b = DefaultMaxTokens
	}
	if b > ceiling {
		b = ceiling
	}
	return b
}
