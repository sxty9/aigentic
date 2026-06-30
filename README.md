# aigentic

The first **derived prizm** (see [`github.com/sxty9/prizm`](../prizm)). A prizm is the
triple `(Request R, Processor P, Graveyard G)` with `proc((R,P,G)) -> Response`. aigentic
declares four processors behind **one consolidated request header**, reads server-local
files into context through a **graveyard substrate**, and serves it all behind the shared
**holistic session**.

```
                 Request (Header.Kind = "choose")
                            │
                        ┌───┴────┐
                        │ choose │   router — complexity (ollama, else heuristic) picks
                        └───┬────┘   ollama-vs-cli; subscription load picks cli-vs-api
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌──────────┐ ┌────────────┐ ┌────────────┐
        │  ollama  │ │ claude-cli │ │ claude-api │   leaves — answer directly
        └──────────┘ └────────────┘ └────────────┘
```

## The triple

- **P — the processors + one header** (`aigentic/`). Four kinds (`ollama`, `claude-cli`,
  `claude-api`, `choose`) share ONE `Request`/`Result` schema (the derived Header₁ inside
  prizm's opaque Data₀), so `choose` forwards a request **verbatim** to any leaf via
  `subprizm.SpawnTyped`. The header carries only what the base does not (prompt, paths,
  output format, model override, token guard, the nested Claude knobs `claude.effort`, and
  the router knobs) — routing, identity, correlation, the depth guard, the envelope codec
  and fan-out all come from the base. The model id already encodes the model version, so
  there is no separate version field; `claude.effort` (`low`…`max`) is applied by the
  `claude-api` leaf as `output_config.effort` and ignored by the others.
- **G — the graveyard** (`aigentic/context.go` + `graveyard/lakegrave/`). `Request.Paths`
  are confined under `<root>/<Subject>` (symlink/traversal-safe, per-caller isolation),
  filtered (binary/size/noise), stored by content-`Ref` for provenance, and assembled into
  a token-budgeted context block ahead of the prompt. The backend is any
  `graveyard.Graveyard`: the in-memory stub by default, or **lakearch** (content-addressed,
  append-only, via cgo) with `-tags lakearch`.
- **R — the holistic shell** (`backend/`). `aigenticd` validates the shared HS256 session
  (the `h_access` cookie) with no RPC, resolves rights live from Linux groups, enforces
  CSRF, and routes on `Header.Kind`. The paid Claude API (and `choose`, which may pick it)
  additionally requires the `hp_aigentic_api` right. The Anthropic key itself is **admin-managed
  at runtime** (set in the dashboard, persisted `0600`), so no secret needs baking into the unit.

## Layout

```
aigentic/                 the SDK: one Request/Result + the four processors + context (G2)
  aigentic.go               the consolidated header, kinds, limits, ErrProcessorUnavailable
  ollama.go / claudeapi.go / claudecli.go   the three leaves (injectable engines)
  choose.go                 the router (classify → policy → availability fallback)
  context.go                Paths → confined, provenance-stored, budgeted context
graveyard/lakegrave/      lakearch graveyard.Graveyard via cgo (build tag `lakearch`)
backend/
  cmd/aigenticd/            the daemon (env-driven config; memory|lakearch graveyard)
  internal/api/             HTTP surface: guard() → registry.Route; 503/404/… mapping
  internal/auth/            shared-JWT session validation (copied verbatim per service)
  internal/rights/          hp_aigentic_* group constants (mirror permissions/aigentic.json)
  internal/grave/           graveyard backend selector (memory default; lakearch tagged)
permissions/aigentic.json the rights manifest declared to privleg
ui/                       the @holistic/ui dashboard plugin
service                   the holistic CLI (auto-detects id from permissions/aigentic.json)
```

`prizm` is resolved locally via a `replace` directive (it has no published tags yet).

## Build & test

```bash
go build ./... && go vet ./... && go test ./...          # default: pure Go, in-memory graveyard
go build -tags lakearch ./... && go test -tags lakearch ./...   # + the lakearch cgo backend
```

The engine and HTTP tests stub every dependency (httptest for ollama/Anthropic, a fake
exec runner for the CLI, a minted JWT for the shell), so they pass with **no ollama, no
`ANTHROPIC_API_KEY`, no CLI login**. The lakearch tests link `liblakearch_ffi` and do a
real append→get round trip.

## Run

```bash
HOLISTIC_SECRET=dev-secret ./aigenticd --listen 127.0.0.1:8781
# health is unauthenticated; everything else needs the holistic h_access cookie.
curl localhost:8781/api/services/aigentic/health   # {"ok":true}
```

Config is env-driven (set by the systemd unit): `OLLAMA_HOST`, `AIGENTIC_OLLAMA_MODEL`,
`AIGENTIC_CLAUDE_MODEL`, `AIGENTIC_CLAUDE_BIN`, `AIGENTIC_MAX_TOKENS`, `AIGENTIC_CONTEXT_ROOT`,
`AIGENTIC_GRAVEYARD` (`memory`|`lakearch`), `AIGENTIC_GRAVEYARD_DIR`, `AIGENTIC_ADMIN_GROUP`,
`AIGENTIC_SECRET_FILE`. `ANTHROPIC_API_KEY` is now only a **bootstrap** for the key store (see
below). Each engine self-reports `ErrProcessorUnavailable` (→ 503) when its backing service or
secret is absent, so a partial environment still yields a runnable service.

### Anthropic API key — admin-managed in the dashboard

The paid `claude-api` key is **set by an admin in the dashboard**, not baked into the unit. An
admin-only panel (`GET`/`POST /api/services/aigentic/secret`, CSRF-guarded) stores it; the
daemon writes it to `AIGENTIC_SECRET_FILE` (default `$STATE_DIRECTORY/anthropic.key`, i.e.
`/var/lib/aigentic/anthropic.key`) at mode `0600`, and the `claude-api` leaf reads it **per
request** (`ClaudeAPIConfig.KeyFunc`), so a change takes effect with no restart. The key value
is never returned — status shows only `configured`, `source` (`store`|`env`) and a masked hint
(`sk-ant-…1234`). `ANTHROPIC_API_KEY`, if set, seeds the store as a read-only bootstrap that an
admin can override (and a clear falls back to). The systemd unit grants the daemon a writable
`StateDirectory=` for this file under `ProtectSystem=strict`.

### `choose` routing — cli-first with a subscription spill

`choose` is **cli-first**: it saturates the flat-rate subscription (medium AND high →
`claude-cli`) and keeps `ollama` only for trivial work (low). It reaches the metered
`claude-api` two ways: the availability fallback (cli down → api), and an opt-in
**subscription spill** — it estimates the Claude subscription's rolling-window token usage
from the local Claude Code transcripts (`~/.claude/projects/**/*.jsonl`, summing the
trailing window) and, when usage is at/above a threshold, routes to `claude-api` to spare
dev headroom. `Result.decision` reports `spilled` + `cliUsage` when this fires.

Opt-in env: `AIGENTIC_CLI_BUDGET_5H` (token cap for the window; unset ⇒ spill off,
pure cli-first), `AIGENTIC_CLI_SPILL_AT` (default `0.80` = keep 20% headroom),
`AIGENTIC_CLI_PROJECTS_DIR` (default `$HOME/.claude/projects`), `AIGENTIC_CLI_WINDOW`
(default `5h`). **Caveat:** the daemon must be able to read `~/.claude` (run as the
logged-in user, or relax `ProtectHome` in the unit) — the same condition `claude-cli`
already needs.

## Deploy as a holistic service

```bash
sudo ./service setup     # build the daemon, wire systemd + Caddy, declare rights, link the UI
./service status
```

To use lakearch as the graveyard, build with `-tags lakearch` and set
`AIGENTIC_GRAVEYARD=lakearch`.
