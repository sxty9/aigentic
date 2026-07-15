package aigentic

import "testing"

// num_ctx is a FIXED size from CtxCap (contextMode), never sized per prompt — a varying value
// makes ollama reload the model on every request, which thrashed a crawl to a standstill.
func TestNumCtxFixed(t *testing.T) {
	if got := newOllamaClient(OllamaConfig{CtxCap: func() int { return 12288 }}).numCtx(); got != 12288 {
		t.Errorf("compact num_ctx=%d, want 12288", got)
	}
	if got := newOllamaClient(OllamaConfig{CtxCap: func() int { return 32768 }}).numCtx(); got != 32768 {
		t.Errorf("large num_ctx=%d, want 32768", got)
	}
	// nil CtxCap → the default.
	if got := newOllamaClient(OllamaConfig{}).numCtx(); got != defaultMaxCtx {
		t.Errorf("default num_ctx=%d, want %d", got, defaultMaxCtx)
	}
	// A zero/invalid cap also falls back to the default.
	if got := newOllamaClient(OllamaConfig{CtxCap: func() int { return 0 }}).numCtx(); got != defaultMaxCtx {
		t.Errorf("zero cap num_ctx=%d, want %d", got, defaultMaxCtx)
	}
}
