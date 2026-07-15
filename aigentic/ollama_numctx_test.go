package aigentic

import (
	"strings"
	"testing"
)

func TestNumCtxSizing(t *testing.T) {
	single := newOllamaClient(OllamaConfig{CtxCap: func() int { return 12288 }})

	// A tiny prompt sizes down to the floor.
	if got := single.numCtx("sys", "hi", 100); got != minNumCtx {
		t.Errorf("tiny prompt num_ctx=%d, want %d", got, minNumCtx)
	}

	// A huge prompt clamps to the (single-gpu) ceiling.
	big := strings.Repeat("x", 200_000)
	if got := single.numCtx("", big, 1024); got != 12288 {
		t.Errorf("huge prompt num_ctx=%d, want 12288 (capped)", got)
	}

	// A medium prompt rounds up to a whole 2k block within the cap.
	med := strings.Repeat("x", 12_000) // ~4000 tokens
	if got := single.numCtx("", med, 512); got%2048 != 0 || got < 4096 || got > 12288 {
		t.Errorf("medium prompt num_ctx=%d, want a 2k block in [4096,12288]", got)
	}

	// multi-gpu cap allows a larger window for the same huge prompt.
	multi := newOllamaClient(OllamaConfig{CtxCap: func() int { return 32768 }})
	if got := multi.numCtx("", big, 1024); got != 32768 {
		t.Errorf("huge prompt (multi) num_ctx=%d, want 32768", got)
	}

	// nil CtxCap falls back to the default ceiling.
	def := newOllamaClient(OllamaConfig{})
	if got := def.numCtx("", big, 1024); got != defaultMaxCtx {
		t.Errorf("nil cap huge num_ctx=%d, want %d", got, defaultMaxCtx)
	}
}
