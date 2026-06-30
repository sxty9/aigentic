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
	"github.com/sxty9/aigentic/backend/internal/grave"
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

	// The Anthropic API key is admin-managed at runtime (set via the dashboard), persisted
	// 0600 under the systemd StateDirectory. ANTHROPIC_API_KEY seeds it as a bootstrap.
	sec := secretstore.New(secretPath(), os.Getenv("ANTHROPIC_API_KEY"))

	reg := prizm.NewRegistry(*maxDepth)
	if err := aigentic.Register(reg, g, configFromEnv(sec)); err != nil {
		log.Fatalf("aigenticd: %v", err)
	}

	srv := &http.Server{
		Handler:           api.New(v, reg, sec).Handler(),
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
func configFromEnv(sec *secretstore.Store) aigentic.Config {
	ollama := aigentic.OllamaConfig{
		BaseURL: os.Getenv("OLLAMA_HOST"),
		Model:   os.Getenv("AIGENTIC_OLLAMA_MODEL"),
	}
	return aigentic.Config{
		MaxTokens:       atoi(os.Getenv("AIGENTIC_MAX_TOKENS")),
		MaxContextBytes: atoi(os.Getenv("AIGENTIC_MAX_CONTEXT_BYTES")),
		// ContextRoot left empty => Config.limits() reads AIGENTIC_CONTEXT_ROOT / default.
		Ollama: ollama,
		ClaudeCLI: aigentic.ClaudeCLIConfig{
			Bin:   os.Getenv("AIGENTIC_CLAUDE_BIN"),
			Model: os.Getenv("AIGENTIC_CLAUDE_CLI_MODEL"),
		},
		ClaudeAPI: aigentic.ClaudeAPIConfig{
			BaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
			// The key comes from the admin-managed store (seeded by ANTHROPIC_API_KEY), read
			// per request so an admin change takes effect without a restart.
			KeyFunc: sec.Get,
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
		},
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
