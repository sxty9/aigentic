# aigentic — working notes

aigentic is a **derived prizm** (framework: `../prizm`, module `github.com/sxty9/prizm`,
resolved via a `replace` directive — no prizm tags yet). The triple is `(Request, Processor,
Graveyard)`; `proc((R,P,G)) -> Response`.

## Verify

```bash
go build ./... && go vet ./... && go test ./...
go build -tags lakearch ./... && go test -tags lakearch ./...   # cgo lakearch backend
python3 ../holistic/services/dashboard/lib/holistic-perms.py validate ./permissions
```

## Architecture

- **One header for all four kinds.** `ollama`, `claude-cli`, `claude-api`, `choose` share
  `aigentic.Request`/`aigentic.Result`. Add domain fields here only if the base Header₀
  doesn't already cover them (it covers routing/Kind, ID, Subject, Trace/depth, Format,
  and — via subprizm/the graveyard — fan-out and large-payload storage).
- **choose is a router, not a leaf.** It is registered `WithSpawner(reg)` and forwards the
  same `Request` to a leaf via `subprizm.SpawnTyped`. The depth guard and correlation id
  come from the base; never re-implement them. Two axes: **complexity** (ollama-vs-cli, the
  classifier/heuristic) and **subscription load** (cli-vs-api). Default policy is cli-first
  (low→ollama, medium/high→cli); `claude-api` is reached only via the availability fallback
  or the opt-in **subscription spill** — `cliusage.go` sums the abo's rolling-window tokens
  from `~/.claude/projects/**/*.jsonl` and, at/above `SpillAt`, routes cli→api to keep dev
  headroom. Spill is off unless `AIGENTIC_CLI_BUDGET_5H` is set, and needs `~/.claude` read
  access (same constraint as `claude-cli`).
- **Engines are injectable** (`baseURL`/`*http.Client`/`ExecRunner` fields) so tests stub
  them — keep it that way; the suite must pass with no ollama/API key/CLI login.
- **G: path context.** `context.go` confines `Request.Paths` under `<ContextRoot>/<Subject>`
  (Subject is server-stamped; never trust the wire), stores bytes by content-`Ref` for
  provenance, and budgets the context. The lakearch backend (`graveyard/lakegrave/`, cgo)
  returns a Datum's **canonical CBOR** (`{0: bstr}` leaf) from `get` — `unwrapLeaf` recovers
  the raw payload for a faithful blob round-trip. lakearch is content-addressed: `Put`
  ignores the supplied ref.
- **R: the shell is copied, not imported.** `backend/internal/auth/auth.go` is vendored
  verbatim (service-agnostic). Subject and depth are server-authoritative.
- **The Anthropic key is admin-managed at runtime, not env-baked.** `backend/internal/secret`
  persists it to `AIGENTIC_SECRET_FILE` (default `$STATE_DIRECTORY/anthropic.key`, `0600`); the
  `claude-api` leaf reads it per request via `ClaudeAPIConfig.KeyFunc` (a change needs no
  restart). The `/secret` endpoints are **admin-only + CSRF** and never return the key — only
  `configured`/`source`/masked `hint`. `ANTHROPIC_API_KEY` is just a bootstrap (a stored key
  overrides it; a clear falls back to it). The unit needs `StateDirectory=` because
  `ProtectSystem=strict` mounts everything else read-only.

## Rules

1. Keep three things in sync: `permissions/aigentic.json` ⇄ `internal/rights` ⇄ the UI right
   constants (`hp_aigentic_run`, `hp_aigentic_api`).
2. The HTTP shell never decodes Data — it routes on `Header.Kind`. The paid-API right gate
   in `run()` reads `Header.Kind` only (the routing field), never Data.
3. Engines map unavailability to `aigentic.ErrProcessorUnavailable` (→ 503), bad input to
   `prizm.ErrInvalidRequest` (→ 400).
4. The lakearch backend lives behind the `lakearch` build tag so the default build stays
   pure-Go (no C toolchain / library needed).
5. UI may import only `@holistic/ui` and `react`. The daemon runs unprivileged.
6. The Anthropic key is a write-only secret: admin-only + CSRF to set/clear, never returned in
   a response or logged (only `configured`/`source`/masked `hint`). Keep `secret.Store` the sole
   path that touches the key file.

## Known environment gaps (this host)

`ollama` is installed (`qwen2.5:0.5b`); `ANTHROPIC_API_KEY` is unset and no key is stored — set
one via the dashboard's admin key panel (or `AIGENTIC_SECRET_FILE`) before `claude-api` works;
the `claude` CLI is present but its subscription login lives in a human `~/.claude` (the daemon
runs as an unprivileged service user) — so for real `claude-cli` runs the daemon must run as a
logged-in user or a provisioned service account.
