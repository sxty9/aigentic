# aigentic — working notes

aigentic is a **derived prizm** (framework: `../prizm`, module `github.com/sxty9/prizm`,
resolved via a `replace` directive — no prizm tags yet). The triple is `(Request, Processor,
Graveyard)`; `proc((R,P,G)) -> Response`.

## Verify

```bash
go build ./... && go vet ./... && go test ./...
go build -tags lakearch ./... && go test -tags lakearch ./...   # cgo lakearch backend
go build -tags scheme ./... && go test -tags scheme ./...       # cgo scheme backend
L=../holistic/services/dashboard/lib
python3 $L/holistic-perms.py validate ./permissions   # rights manifest
python3 $L/holistic-config.py validate ./config       # configuration manifest
python3 $L/holistic-usage.py  validate ./usage        # consumption (token/compute/memory/storage)
python3 $L/holistic-mcp.py    validate ./mcp          # MCP tool manifest
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
- **Images are a precondition, not a complexity axis.** A request carrying images (`Inline`
  media `image/*`) may only go to an engine that can SEE them: the router drops every blind
  candidate from the chain BEFORE forwarding, so an image never reaches a text-only model that
  would fabricate a description. Vision is a PROPERTY of the machine, probed not name-listed —
  the Claude leaves see images; the ollama leaf only when its resolved model advertises
  `"vision"` via `/api/show` (`ChooseConfig.VisionForKind`, wired by `VisionResolver`; the
  `NewOllama` leaf re-checks and delivers the bytes on the chat `images` field). No capable
  engine ⇒ the named `ErrNoVisionEngine` (→ 422), never a silent fallback; the choose router
  stays gated by `hp_aigentic_api`, so a subject without the cost right can't reach it at all.
  Unread non-image media (e.g. a PDF on the local engine) is NAMED in the answer, not only in
  provenance (`finalize`/`noteMediaGap`).
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
- **Central interface manifests mirror one shape.** `permissions/`, `config/`, `usage/` and
  `mcp/` each hold one `aigentic.json` drop-in that the dashboard's central tabs aggregate;
  `./service setup` installs them into `/etc/holistic/{permissions,config,usage,mcp}.d/`. The
  service tabs stay about the user's experience — rights, config and telemetry live centrally.
- **Consumption is reported, not evaluated.** `backend/internal/usage` accumulates every leaf
  run's token `Usage` (wired via `Config.OnUsage` → a P-layer metering decorator in
  `register.go`, so the shell never decodes Data) and periodically writes tokens + CPU + RSS +
  on-disk bytes into the passive pool `/var/lib/holistic/usage/aigentic.json`. Best-effort: an
  unwritable pool never crashes the daemon. `choose` is not metered (its picked leaf, reached
  through the registry, is — a routed run counts once).
- **MCP is a second door onto the registry.** `backend/internal/mcp` is a domain-agnostic
  JSON-RPC transport; `backend/internal/api/mcp.go` exposes the one tool `aigentic.ask` at the
  fixed server-side path `…/aigentic/mcp`, session-authed, gated by the SAME rights as `/run`,
  and routed through the P-layer `aigentic.Route` (never decoding Data in the shell). The tool
  is declared in `mcp/aigentic.json` for the central MCP registry.
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
   in `run()` reads `Header.Kind` only (the routing field), never Data. A non-passthrough
   surface that needs a typed `Result` (the MCP door) goes through the P-layer `aigentic.Route`
   / the metering decorator, which own the decode — the shell still never touches Data.
3. Engines map unavailability to `aigentic.ErrProcessorUnavailable` (→ 503), bad input to
   `prizm.ErrInvalidRequest` (→ 400).
4. The lakearch backend lives behind the `lakearch` build tag so the default build stays
   pure-Go (no C toolchain / library needed).
5. UI may import only `@holistic/ui` and `react`. The daemon runs unprivileged.
6. The Anthropic key is a write-only secret: admin-only + CSRF to set/clear, never returned in
   a response or logged (only `configured`/`source`/masked `hint`). Keep `secret.Store` the sole
   path that touches the key file.
7. Every MCP tool is covered by a right (`mcp/aigentic.json` names an `hp_*` group) and re-checks
   it in the handler — the same gates as `/run`, including the paid-API check on `claude-api`. Add
   a tool only by declaring it in the manifest AND enforcing its right in `backend/internal/api/mcp.go`.
8. Consumption telemetry is best-effort and passive: `usage.Reporter` only ever WRITES numbers to
   the pool; it must never fail a request or the daemon, and nothing interprets the pool in-repo.

## Operational prerequisites

Every engine self-reports `aigentic.ErrProcessorUnavailable` (→ 503) when its backing service
or secret is absent, so the test suite and a partial deployment both run without any of them. A
**real** run of each backend needs its dependency provisioned in the runtime environment (never
baked into the repo):

- `ollama` — a reachable ollama server (`OLLAMA_HOST`, default `localhost:11434`) with the
  configured model pulled.
- `claude-api` — an Anthropic key set via the dashboard admin key panel (persisted to
  `AIGENTIC_SECRET_FILE`); `ANTHROPIC_API_KEY` only bootstraps the store.
- `claude-cli` — the daemon must run under an identity whose `~/.claude` holds a logged-in
  Claude subscription; an unprivileged service user without one cannot make real cli runs (the
  same `~/.claude` read access the `choose` subscription-spill needs).
- **consumption reporting** — the daemon writes `/var/lib/holistic/usage/aigentic.json`. This
  needs the shared pool dir to exist and be group-writable by `holistic` (setgid) and the unit to
  grant `ReadWritePaths=/var/lib/holistic/usage` (both done by `./service setup`, which also adds
  the service user to `holistic`). Without them the reporter simply no-ops; the service runs
  unchanged and the Consumption tab shows nothing for aigentic.
- **MCP** — `…/aigentic/mcp` is session-authed today (addressable via the central infrastructure
  / same-origin). Minting bearer tokens for external MCP clients (as hosuto does) is a planned
  follow-up; it is not required for the central registry to see the declared tools.

<!-- BEGIN HOLISTIC AXIOMS -->
# Holistic axioms & implementation rules

The binding Holistic axioms and implementation rules are **not reproduced in this repository**. They
are maintained centrally in Holistic's shared axiom store — the single source of truth for the
constitution — and reach every implementation through the prompt that commissions it, so the current
wording always governs, independent of this file's age. Only this pointer belongs in the repo; the
store's concrete location lives in runtime configuration, never here. Do not paste the axiom text
back into this file or keep a hand-maintained copy.
<!-- END HOLISTIC AXIOMS -->

<!-- holistic:constitution:begin -->
# Holistic — Verfassung

Für dieses Repository gelten die Holistic-Axiome und Implementierungsregeln.
Ihr verbindlicher Wortlaut wird nicht hier geführt, sondern mit jedem
Implementierungsauftrag mitgeliefert. So gilt immer der aktuelle Stand.

**Arbeitest du im Auftrag von Mercury:** Der Wortlaut steht vollständig in
deinem Prompt. Er hat Vorrang vor jeder anderen Fassung, die dir begegnet.

**Arbeitest du in einer von Hand geöffneten Sitzung:** Implementiere nicht
selbst. Lege die Arbeit als ToDo in Mercury an und führe es aus — dann kommt
der verbindliche Wortlaut auf dem regulären Weg. Der Verfassungs-Bestand wird
in der Laufzeit-Konfiguration der Instanz benannt.
<!-- holistic:constitution:end -->
