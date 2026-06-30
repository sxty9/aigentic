package aigentic

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cliUsageEstimator estimates the Claude subscription's rolling-window token consumption
// from Claude Code's local transcripts (~/.claude/projects/**/*.jsonl). Every assistant
// line records a timestamp and message.usage, so summing the trailing window approximates
// how close the subscription is to its cap — across BOTH the operator's own dev sessions
// and aigentic's claude-cli calls. The choose router consults it to spill to claude-api
// before the subscription is exhausted, protecting dev headroom.
//
// It measures consumption (real); the cap (the plan's window allowance) is configured, so
// utilization = measured / cap. cache_read tokens are deliberately excluded (cheap re-reads
// that weigh little toward the limit) — an approximation that is good enough to brake in
// time. The format is Claude Code's internal, undocumented shape (parsed defensively).
type cliUsageEstimator struct {
	dir       string        // transcripts root to scan
	window    time.Duration // rolling window (e.g. 5h)
	capTokens int64         // configured token cap for the window; <= 0 disables the estimator
	ttl       time.Duration // minimum interval between rescans
	now       func() time.Time

	mu        sync.Mutex
	cachedAt  time.Time
	cached    int64
	haveCache bool
}

// newCLIUsageEstimator builds an estimator with production defaults (60s scan cache,
// wall-clock). Tests construct the struct directly with an injected clock/ttl/dir.
func newCLIUsageEstimator(dir string, window time.Duration, capTokens int64) *cliUsageEstimator {
	if window <= 0 {
		window = 5 * time.Hour
	}
	return &cliUsageEstimator{dir: dir, window: window, capTokens: capTokens, ttl: 60 * time.Second, now: time.Now}
}

// NewCLIUtilization builds the ChooseConfig.Utilization callback from the local Claude Code
// transcripts: dir is the transcripts root (e.g. ~/.claude/projects), window the rolling
// window, capTokens the configured per-window token cap. Returns nil when capTokens <= 0
// (the subscription-spill feature is opt-in), so the router stays purely cli-first.
func NewCLIUtilization(dir string, window time.Duration, capTokens int64) func() (float64, bool) {
	if capTokens <= 0 {
		return nil
	}
	return newCLIUsageEstimator(dir, window, capTokens).utilization
}

// utilization returns used/cap for the trailing window. ok is false when the estimator is
// disabled (cap <= 0) or the transcripts cannot be read — in which case the router must NOT
// spill (treat "unknown" as "stay on cli").
func (e *cliUsageEstimator) utilization() (frac float64, ok bool) {
	if e == nil || e.capTokens <= 0 {
		return 0, false
	}
	used, err := e.windowTokens()
	if err != nil {
		return 0, false
	}
	return float64(used) / float64(e.capTokens), true
}

func (e *cliUsageEstimator) windowTokens() (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if e.haveCache && now.Sub(e.cachedAt) < e.ttl {
		return e.cached, nil
	}
	sum, err := e.scan(now)
	if err != nil {
		return 0, err
	}
	e.cached, e.cachedAt, e.haveCache = sum, now, true
	return sum, nil
}

func (e *cliUsageEstimator) scan(now time.Time) (int64, error) {
	cutoff := now.Add(-e.window)
	var total int64
	err := filepath.WalkDir(e.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil // skip unreadable entries / non-transcripts
		}
		// A file untouched within the window can hold no in-window lines — skip the read.
		if info, ierr := d.Info(); ierr != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		total += sumFileWindow(path, cutoff)
		return nil
	})
	return total, err
}

// sumFileWindow adds input+output+cache_creation tokens for transcript lines whose
// timestamp falls within [cutoff, ∞). Unparseable or field-less lines are skipped.
func sumFileWindow(path string, cutoff time.Time) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var sum int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20) // transcript lines can be large
	for sc.Scan() {
		var line struct {
			Timestamp string `json:"timestamp"`
			Message   struct {
				Usage struct {
					Input         int64 `json:"input_tokens"`
					Output        int64 `json:"output_tokens"`
					CacheCreation int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil || line.Timestamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, line.Timestamp)
		if err != nil || t.Before(cutoff) {
			continue
		}
		sum += line.Message.Usage.Input + line.Message.Usage.Output + line.Message.Usage.CacheCreation
	}
	return sum
}
