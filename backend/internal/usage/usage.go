// Package usage is aigentic's reporter for the holistic consumption standard — the sibling of
// the rights (permissions.d) and configuration (config.d) standards, for CONSUMPTION.
//
// Consumption: usage/<id>.json declares WHICH metrics a service reports (kind + label) →
// /etc/holistic/usage.d/<id>.json; every service WRITES its current measured values into the
// passive report pool /var/lib/holistic/usage/<id>.json; the dashboard's central Consumption tab
// READS every pool file and aggregates. One file, one writer (this daemon), one reader (the
// dashboard), no RPC and no push — the same shape as the JWT secret and the rights groups.
//
// aigentic is the AI-token service, so its most important report is token consumption (kind
// "tokens"); it also reports its own compute (CPU seconds), memory (RSS) and storage (bytes held
// on disk) so load and capacity are judgeable across the landscape from one place. The service
// only REPORTS; the evaluation lives outside it (Consumption axiom: "Der Service meldet nur; die
// Auswertung liegt außerhalb"). The pool is a purely passive store (passive-data-pools axiom).
//
// Telemetry must never crash the daemon, so every write is best-effort: an unwritable pool (a
// fresh host where the dir does not exist yet, or a wrong owner) is swallowed, exactly as the
// dashboard's own reporter swallows it.
package usage

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// PoolDir is the passive report pool the dashboard aggregates. Group-readable by `holistic`.
	PoolDir = "/var/lib/holistic/usage"
	// Group is the Unix group shared by the dashboard and the service daemons (values are 0640).
	Group = "holistic"
	// defaultInterval bounds how stale the pool may be. Short enough that an admin watching the
	// Consumption tab (which polls every 10s) sees fresh numbers within a tick or two.
	defaultInterval = 30 * time.Second
	// counterFile persists the cumulative token counters under the state dir, so a restart does
	// not reset "total tokens consumed" — consumption is monotonic by definition.
	counterFile = "usage-counters.json"
)

// Reporter accumulates aigentic's token consumption and periodically writes the full consumption
// snapshot (tokens + compute + memory + storage) into the passive pool. Safe for concurrent use:
// AddTokens is called from every processor run.
type Reporter struct {
	service     string
	poolPath    string
	group       string
	stateDir    string // measured for storage, and where the token counters persist
	counterPath string

	mu        sync.Mutex
	inTokens  int64
	outTokens int64
	dirty     bool // token counters changed since the last persist
}

// New builds a reporter for one service. poolDir/group/stateDir fall back to the standard
// locations when empty. It loads any persisted token counters so totals survive a restart.
func New(service, poolDir, group, stateDir string) *Reporter {
	if poolDir == "" {
		poolDir = PoolDir
	}
	if group == "" {
		group = Group
	}
	r := &Reporter{
		service:     service,
		poolPath:    filepath.Join(poolDir, service+".json"),
		group:       group,
		stateDir:    stateDir,
		counterPath: filepath.Join(stateDir, counterFile),
	}
	r.loadCounters()
	return r
}

// AddTokens records one engine run's token usage. Negative values (never expected) are ignored so
// the cumulative counters stay monotonic. It is the sink wired into aigentic.Config.OnUsage.
func (r *Reporter) AddTokens(in, out int) {
	if in <= 0 && out <= 0 {
		return
	}
	r.mu.Lock()
	if in > 0 {
		r.inTokens += int64(in)
	}
	if out > 0 {
		r.outTokens += int64(out)
	}
	r.dirty = true
	r.mu.Unlock()
}

// Run writes the pool once immediately, then on every tick, and a final time on ctx cancellation,
// so a graceful shutdown flushes the last counts. interval <= 0 uses the default. Blocks until
// ctx is cancelled; run it in a goroutine.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultInterval
	}
	r.Flush()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush()
			return
		case <-t.C:
			r.Flush()
		}
	}
}

// Flush persists the token counters (if changed) and writes the consumption snapshot to the pool.
// Safe to call directly (e.g. one guaranteed synchronous write during shutdown).
func (r *Reporter) Flush() {
	r.persistCounters()
	r.writePool(r.metrics())
}

// metrics builds the current consumption snapshot: cumulative tokens, cumulative CPU seconds,
// current resident memory and total bytes held on disk. Every value is a plain number so the
// aggregator can sum metrics of the same kind across services.
func (r *Reporter) metrics() map[string]any {
	r.mu.Lock()
	in, out := r.inTokens, r.outTokens
	r.mu.Unlock()
	return map[string]any{
		"inputTokens":   in,
		"outputTokens":  out,
		"cpuSeconds":    cpuSeconds(),
		"residentBytes": residentBytes(),
		"storageBytes":  dirBytes(r.stateDir),
	}
}

// writePool atomically writes one report into the passive pool (temp → fsync → rename), 0640 and
// group `holistic` — the same trust boundary as the config values. Best-effort: a missing or
// read-only pool dir is swallowed (telemetry must never crash the daemon); the ./service installer
// provisions the dir and grants the daemon write access through the systemd unit.
func (r *Reporter) writePool(metrics map[string]any) {
	payload := map[string]any{"metrics": metrics, "ts": time.Now().UnixMilli()}
	writeJSONAtomic(r.poolPath, payload, r.group)
}

// persistCounters writes the cumulative token counters under the state dir so a restart does not
// lose them. Only writes when something changed. Best-effort.
func (r *Reporter) persistCounters() {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	payload := map[string]any{"inputTokens": r.inTokens, "outputTokens": r.outTokens}
	r.dirty = false
	r.mu.Unlock()
	if r.counterPath != "" {
		writeJSONAtomic(r.counterPath, payload, "")
	}
}

// loadCounters restores persisted token counters (best-effort; a fresh host starts at zero).
func (r *Reporter) loadCounters() {
	if r.counterPath == "" {
		return
	}
	b, err := os.ReadFile(r.counterPath)
	if err != nil {
		return
	}
	var v struct {
		In  int64 `json:"inputTokens"`
		Out int64 `json:"outputTokens"`
	}
	if json.Unmarshal(b, &v) == nil {
		r.inTokens, r.outTokens = v.In, v.Out
	}
}

// ── measurements ────────────────────────────────────────────────────────────────────────

// cpuSeconds returns the cumulative CPU time (user + system) this process has consumed, in
// seconds. Source: getrusage(RUSAGE_SELF) — the kernel's own accounting, no evaluation of ours.
func cpuSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

// residentBytes returns the process's current resident set size (RSS) in bytes, read from
// /proc/self/statm (field 2 = resident pages). 0 when /proc is unavailable.
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// dirBytes sums the sizes of all regular files under dir — the bytes the service holds on disk
// (its graveyard, per-user chats + credentials, the admin key). 0 for an empty/missing dir. This
// is a report of what is stored, not an evaluation of it (Speichernutzung axiom).
func dirBytes(dir string) int64 {
	if dir == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; a partial measure beats none
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// ── atomic write ────────────────────────────────────────────────────────────────────────

// writeJSONAtomic writes v as JSON to path via a temp file (fsync → rename), chmod 0640, and — when
// group is non-empty and this process can — chgrp to it so the dashboard (same group) can read.
// Every step is best-effort: telemetry never crashes the daemon.
func writeJSONAtomic(path string, v any, group string) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Chmod(tmpName, 0o640)
	if group != "" {
		if g, err := user.LookupGroup(group); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil {
				_ = os.Chown(tmpName, -1, gid) // works for a member of the group; ignored otherwise
			}
		}
	}
	_ = os.Rename(tmpName, path)
}
