// Command aigenticd is the aigentic service daemon. It exposes a small HTTP surface under
// /api/services/aigentic/, validates the shared holistic session (a signed JWT in the
// h_access cookie) without any RPC to the holistic backend, and enforces the holistic
// rights standard. It runs unprivileged behind the holistic Caddy proxy.
//
// It registers the four aigentic processors (ollama, claude-cli, claude-api, choose) over
// a graveyard substrate (in-memory by default; lakearch with -tags lakearch). The registry
// is also the in-process sub-prizm spawner the choose router delegates through.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/api"
	"github.com/sxty9/aigentic/backend/internal/auth"
	"github.com/sxty9/aigentic/backend/internal/chatstore"
	"github.com/sxty9/aigentic/backend/internal/grave"
	"github.com/sxty9/aigentic/backend/internal/hconfig"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/prizm/prizm"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8781", "address to listen on")
	maxDepth := flag.Int("max-depth", prizm.DefaultMaxDepth, "sub-prizm recursion ceiling")
	flag.Parse()

	secret, err := auth.LoadSecret()
	if err != nil {
		log.Fatalf("aigenticd: %v", err)
	}
	// Admin = membership in this group (the single Linux source of truth). The systemd
	// unit sets AIGENTIC_ADMIN_GROUP; the verifier defaults to "sudo" when it is empty.
	v := auth.NewVerifier(secret, os.Getenv("AIGENTIC_ADMIN_GROUP"))

	g, err := grave.Open(os.Getenv("AIGENTIC_GRAVEYARD"), os.Getenv("AIGENTIC_GRAVEYARD_DIR"))
	if err != nil {
		log.Fatalf("aigenticd: %v", err)
	}

	// Credentials are PER-USER: each user links their own Anthropic API key + Claude
	// subscription token from the dashboard (under StateDirectory/users/<user>/, 0600). The
	// global anthropic.key is an optional shared admin fallback; ANTHROPIC_API_KEY seeds it.
	sec := secretstore.New(secretPath(), usersDir(), os.Getenv("ANTHROPIC_API_KEY"))

	// contextMode comes from the central Configuration tab (config/aigentic.json → the dashboard →
	// /var/lib/holistic/config/aigentic.json), read LIVE (5s TTL, no restart). It caps the local
	// context window: "compact" keeps the VRAM footprint lean (best latency); "large" allows long
	// documents (more VRAM, may span both GPUs). The leaf still sizes num_ctx to the actual prompt;
	// this only bounds it.
	hc := hconfig.New("aigentic", "", "")
	ctxCap := func() int {
		if hc.String("contextMode", "compact") == "large" {
			return 32768
		}
		return 12288
	}

	reg := prizm.NewRegistry(*maxDepth)
	if err := aigentic.Register(reg, g, configFromEnv(sec, ctxCap)); err != nil {
		log.Fatalf("aigenticd: %v", err)
	}

	// Per-user chat history shares the per-user state root (same StateDirectory/users/<user>/).
	chats := chatstore.New(usersDir())

	srv := &http.Server{
		Handler: api.New(v, reg, g, sec, func(ctx context.Context) ([]string, error) {
			return aigentic.OllamaModels(ctx, ollamaConfig(ctxCap))
		}, chats).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind synchronously so an "address in use" surfaces here, not in a goroutine.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("aigenticd: listen %s: %v", *listen, err)
	}
	go func() {
		log.Printf("aigenticd listening on %s", *listen)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("aigenticd: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Print("aigenticd stopped")
}

// configFromEnv assembles the processor configuration from the environment (set by the
// systemd unit). Engines self-report ErrProcessorUnavailable when their backing service or
// secret is absent, so a partial environment still yields a runnable service.
func configFromEnv(sec *secretstore.Store, ctxCap func() int) aigentic.Config {
	ollama := ollamaConfig(ctxCap)
	return aigentic.Config{
		MaxTokens:       atoi(os.Getenv("AIGENTIC_MAX_TOKENS")),
		MaxContextBytes: atoi(os.Getenv("AIGENTIC_MAX_CONTEXT_BYTES")),
		// ContextRoot left empty => Config.limits() reads AIGENTIC_CONTEXT_ROOT / default.
		Ollama: ollama,
		ClaudeCLI: aigentic.ClaudeCLIConfig{
			Bin:   os.Getenv("AIGENTIC_CLAUDE_BIN"),
			Model: os.Getenv("AIGENTIC_CLAUDE_CLI_MODEL"),
			// Per-user billing: each request runs `claude` with the requesting user's own credential —
			// their subscription token (CLAUDE_CODE_OAUTH_TOKEN) if linked, else their own API key
			// (ANTHROPIC_API_KEY, never the shared fallback) — in an isolated CLAUDE_CONFIG_DIR. A user
			// with neither makes claude-cli unavailable (choose falls back).
			TokenFunc:     sec.OAuthToken,
			KeyFunc:       sec.UserKey,
			ConfigDirFunc: sec.ConfigDir,
			// The allow-list of MCP servers an agentic run may attach (name → URL). The Ask-AI tab
			// attaches "hosuto" here; the URL is server-side so the wire cannot aim the CLI elsewhere.
			MCPProviders: mcpProvidersFromEnv(),
		},
		ClaudeAPI: aigentic.ClaudeAPIConfig{
			BaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
			// Per-user key: the requesting user's own Anthropic key (else the shared/global
			// fallback, else the env bootstrap), read per request so changes take effect live.
			KeyFunc: sec.Key,
			Model:   os.Getenv("AIGENTIC_CLAUDE_MODEL"),
		},
		Choose: aigentic.ChooseConfig{
			// Classify via a cheap, direct ollama call; choose falls back to a heuristic
			// when ollama is unreachable, so this is safe even with ollama absent.
			Classify: aigentic.OllamaClassifier(ollama, os.Getenv("AIGENTIC_CLASSIFIER_MODEL")),
			// Subscription-spill: prefer claude-cli, but route to claude-api once the abo's
			// rolling-window usage nears its cap (read from ~/.claude transcripts). Opt-in:
			// nil Utilization unless AIGENTIC_CLI_BUDGET_5H is set, so default stays cli-first.
			Utilization: aigentic.NewCLIUtilization(cliProjectsDir(), cliWindow(), atoi64(os.Getenv("AIGENTIC_CLI_BUDGET_5H"))),
			SpillAt:     atof(os.Getenv("AIGENTIC_CLI_SPILL_AT")),
			// Complexity→leaf mapping. All-empty => built-in cli-first default (backward
			// compatible). Set AIGENTIC_POLICY_{LOW,MEDIUM,HIGH}=ollama for a local-first tier.
			Policy: policyFromEnv(),
			// Local-model tier: run a bigger local model for harder buckets when the ollama
			// leaf is picked. Medium/default comes from the ollama leaf (AIGENTIC_OLLAMA_MODEL).
			LocalModels: aigentic.LocalModelTier{
				Low:  os.Getenv("AIGENTIC_OLLAMA_LOW_MODEL"),
				High: os.Getenv("AIGENTIC_OLLAMA_HIGH_MODEL"),
			},
		},
	}
}

// mcpProvidersFromEnv parses AIGENTIC_MCP_PROVIDERS — a comma- or whitespace-separated list of
// name=url pairs, e.g. "hosuto=http://127.0.0.1:8779/api/services/hosuto/mcp". It is the allow-list
// of MCP servers an agentic run may attach by name; unset => no providers, so the feature stays inert
// until a deployment opts in. The plain form (no JSON) keeps it clean in a systemd Environment= line.
func mcpProvidersFromEnv() map[string]string {
	raw := strings.TrimSpace(os.Getenv("AIGENTIC_MCP_PROVIDERS"))
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		i := strings.IndexByte(field, '=') // split on the first = so a URL query (also =) survives
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(field[:i])
		url := strings.TrimSpace(field[i+1:])
		if name != "" && url != "" {
			out[name] = url
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ollamaConfig builds the shared local-ollama config (engine + classifier + model listing).
// ctxCap (may be nil) caps the per-request context window — wired to the live gpuMode setting.
func ollamaConfig(ctxCap func() int) aigentic.OllamaConfig {
	return aigentic.OllamaConfig{
		BaseURL: os.Getenv("OLLAMA_HOST"),
		Model:   os.Getenv("AIGENTIC_OLLAMA_MODEL"),
		CtxCap:  ctxCap,
	}
}

// policyFromEnv builds the complexity→leaf routing policy from the environment. Each var is a
// kind ("ollama"|"claude-cli"|"claude-api"); all-empty yields the zero RoutePolicy, which the
// router replaces with its built-in cli-first default (so behaviour is unchanged by default).
func policyFromEnv() aigentic.RoutePolicy {
	return aigentic.RoutePolicy{
		Low:    prizm.Kind(os.Getenv("AIGENTIC_POLICY_LOW")),
		Medium: prizm.Kind(os.Getenv("AIGENTIC_POLICY_MEDIUM")),
		High:   prizm.Kind(os.Getenv("AIGENTIC_POLICY_HIGH")),
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// cliProjectsDir is the Claude Code transcripts root scanned for subscription usage.
func cliProjectsDir() string {
	if d := os.Getenv("AIGENTIC_CLI_PROJECTS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// cliWindow is the rolling subscription window; defaults to 5h (the abo session window).
func cliWindow() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AIGENTIC_CLI_WINDOW")); err == nil && d > 0 {
		return d
	}
	return 5 * time.Hour
}

// secretPath is where the admin-managed Anthropic key is persisted. Default: the systemd
// StateDirectory (the unit makes /var/lib/aigentic writable under ProtectSystem=strict and
// exports $STATE_DIRECTORY). Override with AIGENTIC_SECRET_FILE.
func secretPath() string {
	if p := os.Getenv("AIGENTIC_SECRET_FILE"); p != "" {
		return p
	}
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		// systemd may pass a colon-separated list; take the first entry.
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		return filepath.Join(d, "anthropic.key")
	}
	return "/var/lib/aigentic/anthropic.key"
}

// usersDir is the per-user credential root (api.key + claude-oauth.token + claude/ per user).
// Defaults to the systemd StateDirectory's users/ subdir; override with AIGENTIC_USERS_DIR.
func usersDir() string {
	if p := os.Getenv("AIGENTIC_USERS_DIR"); p != "" {
		return p
	}
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		return filepath.Join(d, "users")
	}
	return "/var/lib/aigentic/users"
}
