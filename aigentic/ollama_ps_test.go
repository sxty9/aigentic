package aigentic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOllamaPS verifies the /api/ps mirror: field mapping, the fully-on-GPU derivation (size_vram
// vs size) and the keep-alive time-left computed against an injected "now".
func TestOllamaPS(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 17, 0, 0, time.UTC)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen2.5:14b","size":10486326230,"size_vram":10486326230,"context_length":6144,"expires_at":"2026-07-15T15:47:00Z"},
			{"name":"big:32b","size":19851349669,"size_vram":12000000000,"context_length":32768,"expires_at":"2026-07-15T15:20:00Z"}
		]}`))
	}))
	defer ts.Close()

	got, err := newOllamaClient(OllamaConfig{BaseURL: ts.URL}).ps(context.Background(), now)
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2", len(got))
	}

	m0 := got[0] // fully resident on the GPU(s), 30 min of keep-alive left
	if m0.Name != "qwen2.5:14b" || m0.ContextLength != 6144 || !m0.FullyOnGPU {
		t.Errorf("m0 = %+v", m0)
	}
	if m0.ExpiresInSec != 1800 { // 15:47 − 15:17
		t.Errorf("m0.ExpiresInSec = %d, want 1800", m0.ExpiresInSec)
	}
	if m0.ExpiresAt == "" {
		t.Errorf("m0.ExpiresAt should be set")
	}

	m1 := got[1] // size_vram < size ⇒ spilled to CPU, not fully on GPU
	if m1.FullyOnGPU {
		t.Errorf("m1 should not be fully on GPU (size_vram %d < size %d)", m1.VRAMBytes, m1.SizeBytes)
	}
	if m1.ExpiresInSec != 180 { // 15:20 − 15:17
		t.Errorf("m1.ExpiresInSec = %d, want 180", m1.ExpiresInSec)
	}
}

// TestOllamaPSUnavailable checks that an unreachable daemon maps to ErrProcessorUnavailable, so the
// HTTP shell degrades to an empty readout rather than a hard error.
func TestOllamaPSUnavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ts.Close() // closed ⇒ connection refused

	_, err := newOllamaClient(OllamaConfig{BaseURL: ts.URL}).ps(context.Background(), time.Now())
	if !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("err = %v, want ErrProcessorUnavailable", err)
	}
}
