package aigentic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTranscript(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
}

// usageLine builds one assistant transcript line in Claude Code's shape.
func usageLine(ts time.Time, in, out, cacheCreation, cacheRead int) map[string]any {
	return map[string]any{
		"type":      "assistant",
		"timestamp": ts.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_creation_input_tokens": cacheCreation,
				"cache_read_input_tokens":     cacheRead,
			},
		},
	}
}

// The estimator sums input+output+cache_creation within the window, ignores cache_read and
// out-of-window lines, and divides by the configured cap.
func TestCLIUsageWindowSum(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, filepath.Join(dir, "a.jsonl"), []map[string]any{
		usageLine(now.Add(-1*time.Hour), 100, 50, 10, 9999), // in-window => 160 (cache_read 9999 ignored)
		usageLine(now.Add(-2*time.Hour), 200, 100, 0, 0),    // in-window => 300
		usageLine(now.Add(-10*time.Hour), 1000, 1000, 0, 0), // outside 5h window => ignored
		{"type": "system", "note": "no usage"},              // unrelated line => skipped
	})

	est := &cliUsageEstimator{dir: dir, window: 5 * time.Hour, capTokens: 1000, ttl: time.Minute, now: func() time.Time { return now }}

	if used, _ := est.windowTokens(); used != 460 {
		t.Fatalf("window tokens = %d, want 460", used)
	}
	frac, ok := est.utilization()
	if !ok || frac < 0.459 || frac > 0.461 {
		t.Fatalf("utilization = %v ok=%v, want ~0.46", frac, ok)
	}
}

// cap <= 0 disables the estimator (ok=false), and a result is cached for the TTL so a
// mid-window file change is not re-read until the TTL elapses.
func TestCLIUsageDisabledAndCaching(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	off := &cliUsageEstimator{dir: dir, window: time.Hour, capTokens: 0, ttl: time.Minute, now: nowFn}
	if _, ok := off.utilization(); ok {
		t.Fatal("cap<=0 must disable the estimator")
	}

	path := filepath.Join(dir, "a.jsonl")
	writeTranscript(t, path, []map[string]any{usageLine(now.Add(-time.Minute), 100, 0, 0, 0)})
	est := &cliUsageEstimator{dir: dir, window: time.Hour, capTokens: 1000, ttl: time.Hour, now: nowFn}

	if first, _ := est.windowTokens(); first != 100 {
		t.Fatalf("first scan = %d, want 100", first)
	}
	// Add usage; within the TTL the cached value (100) must be returned, not 600.
	writeTranscript(t, path, []map[string]any{
		usageLine(now.Add(-time.Minute), 100, 0, 0, 0),
		usageLine(now.Add(-time.Minute), 500, 0, 0, 0),
	})
	if second, _ := est.windowTokens(); second != 100 {
		t.Fatalf("cached scan = %d, want 100 (TTL not elapsed)", second)
	}
}
