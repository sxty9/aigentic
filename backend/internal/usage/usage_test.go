package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readPool decodes the pool file the reporter wrote.
func readPool(t *testing.T, poolDir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(poolDir, "aigentic.json"))
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode pool: %v", err)
	}
	return m
}

func TestFlushWritesPoolShape(t *testing.T) {
	pool, state := t.TempDir(), t.TempDir()
	r := New("aigentic", pool, "", state)
	r.AddTokens(12, 34)
	r.Flush()

	got := readPool(t, pool)
	if _, ok := got["ts"].(float64); !ok {
		t.Errorf("pool missing numeric ts: %v", got["ts"])
	}
	metrics, ok := got["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("pool missing metrics object: %v", got["metrics"])
	}
	// Every declared metric id must be present and numeric (the aggregator sums numbers per kind).
	for _, id := range []string{"inputTokens", "outputTokens", "cpuSeconds", "residentBytes", "storageBytes"} {
		if _, ok := metrics[id].(float64); !ok {
			t.Errorf("metric %q missing or non-numeric: %v", id, metrics[id])
		}
	}
	if metrics["inputTokens"].(float64) != 12 || metrics["outputTokens"].(float64) != 34 {
		t.Errorf("token counts = %v/%v, want 12/34", metrics["inputTokens"], metrics["outputTokens"])
	}
}

func TestTokensAreCumulativeAndMonotonic(t *testing.T) {
	pool, state := t.TempDir(), t.TempDir()
	r := New("aigentic", pool, "", state)
	r.AddTokens(10, 20)
	r.AddTokens(5, 0)
	r.AddTokens(-3, -9) // negative never expected; must be ignored, not subtracted
	r.Flush()

	m := readPool(t, pool)["metrics"].(map[string]any)
	if m["inputTokens"].(float64) != 15 {
		t.Errorf("inputTokens = %v, want 15", m["inputTokens"])
	}
	if m["outputTokens"].(float64) != 20 {
		t.Errorf("outputTokens = %v, want 20", m["outputTokens"])
	}
}

func TestCountersPersistAcrossRestart(t *testing.T) {
	pool, state := t.TempDir(), t.TempDir()
	r := New("aigentic", pool, "", state)
	r.AddTokens(100, 200)
	r.Flush() // persists counters under state

	// A fresh reporter over the same state dir loads the persisted totals (a restart).
	r2 := New("aigentic", pool, "", state)
	r2.AddTokens(1, 2)
	r2.Flush()

	m := readPool(t, pool)["metrics"].(map[string]any)
	if m["inputTokens"].(float64) != 101 || m["outputTokens"].(float64) != 202 {
		t.Errorf("after restart tokens = %v/%v, want 101/202", m["inputTokens"], m["outputTokens"])
	}
}

func TestStorageBytesCountsStateDir(t *testing.T) {
	pool, state := t.TempDir(), t.TempDir()
	// Two files (11 + 4 bytes) plus a nested dir.
	if err := os.WriteFile(filepath.Join(state, "a.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(state, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New("aigentic", pool, "", state)
	r.Flush()
	got := readPool(t, pool)["metrics"].(map[string]any)["storageBytes"].(float64)
	// At least the 15 bytes we wrote (the persisted counter file adds a little more).
	if got < 15 {
		t.Errorf("storageBytes = %v, want >= 15", got)
	}
}

func TestMeasurementsAreSane(t *testing.T) {
	if cpuSeconds() < 0 {
		t.Errorf("cpuSeconds negative: %v", cpuSeconds())
	}
	if residentBytes() <= 0 {
		t.Errorf("residentBytes should be positive on linux, got %v", residentBytes())
	}
	if dirBytes("") != 0 {
		t.Errorf("dirBytes(\"\") = %v, want 0", dirBytes(""))
	}
}
