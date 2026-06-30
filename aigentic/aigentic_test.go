package aigentic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// --- stubs: every engine is faked so the suite needs no ollama / API key / CLI login ---

func ollamaStub(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"content": content},
			"prompt_eval_count": 3,
			"eval_count":        5,
		})
	}))
}

func anthropicStub(text string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"usage":   map[string]int{"input_tokens": 7, "output_tokens": 11},
		})
	}))
}

func fakeCLI(result string) ExecRunner {
	return func(_ context.Context, _ string, _ []string, _ string, _ []string) ([]byte, error) {
		return json.Marshal(map[string]any{
			"type":     "result",
			"result":   result,
			"is_error": false,
			"usage":    map[string]int{"input_tokens": 2, "output_tokens": 4},
		})
	}
}

// --- helpers ---

func newReg(t *testing.T, cfg Config) *prizm.Registry {
	t.Helper()
	reg := prizm.NewRegistry(0)
	if err := Register(reg, graveyard.NewMemory(), cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

func route(reg *prizm.Registry, kind prizm.Kind, in Request) (Result, error) {
	data, err := prizm.EncodeData(in)
	if err != nil {
		return Result{}, err
	}
	resp, err := reg.Route(context.Background(), prizm.Request{Header: prizm.Header{Kind: kind}, Data: data})
	if err != nil {
		return Result{}, err
	}
	return prizm.DecodeData[Result](resp.Data)
}

func mustRoute(t *testing.T, reg *prizm.Registry, kind prizm.Kind, in Request) Result {
	t.Helper()
	out, err := route(reg, kind, in)
	if err != nil {
		t.Fatalf("route %s: %v", kind, err)
	}
	return out
}

// TestLeavesShareOneSchema is the central proof: the SAME Request value routes through
// all three leaves and yields the SAME Result type — i.e. one consolidated header serves
// every processor.
func TestLeavesShareOneSchema(t *testing.T) {
	ol := ollamaStub("ollama-says-hi")
	defer ol.Close()
	an := anthropicStub("api-says-hi")
	defer an.Close()

	reg := newReg(t, Config{
		Ollama:    OllamaConfig{BaseURL: ol.URL, Model: "llama-test"},
		ClaudeCLI: ClaudeCLIConfig{Model: "cli-test", Run: fakeCLI("cli-says-hi")},
		ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "test", Model: "api-test"},
	})

	in := Request{Prompt: "hello", Paths: []string{"/srv/x"}, OutputFormat: "text", MaxTokens: 100}

	for _, c := range []struct {
		kind prizm.Kind
		want string
	}{
		{KindOllama, "ollama-says-hi"},
		{KindClaudeCLI, "cli-says-hi"},
		{KindClaudeAPI, "api-says-hi"},
	} {
		got := mustRoute(t, reg, c.kind, in) // identical `in` for every kind
		if got.Output != c.want {
			t.Errorf("%s: output=%q want %q", c.kind, got.Output, c.want)
		}
		if got.Engine != c.kind {
			t.Errorf("%s: engine=%q want %q", c.kind, got.Engine, c.kind)
		}
		if got.Usage.TotalTokens == 0 {
			t.Errorf("%s: usage not reported", c.kind)
		}
	}
}

func TestChooseForce(t *testing.T) {
	an := anthropicStub("forced-api")
	defer an.Close()
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"}})

	got := mustRoute(t, reg, KindChoose, Request{
		Prompt: "anything",
		Choose: &ChooseOptions{Force: KindClaudeAPI},
	})
	if got.Output != "forced-api" {
		t.Fatalf("output=%q", got.Output)
	}
	if got.Decision == nil || got.Decision.Source != "forced" || got.Decision.Picked != KindClaudeAPI {
		t.Fatalf("decision=%+v", got.Decision)
	}
	if got.Engine != KindClaudeAPI {
		t.Fatalf("engine=%q", got.Engine)
	}
}

// No classifier configured (the confirmed local case: ollama absent) => heuristic path.
func TestChooseHeuristicLow(t *testing.T) {
	ol := ollamaStub("low-route")
	defer ol.Close()
	reg := newReg(t, Config{Ollama: OllamaConfig{BaseURL: ol.URL, Model: "m"}})

	got := mustRoute(t, reg, KindChoose, Request{Prompt: "hi there"}) // short, no paths => low
	if got.Decision == nil || got.Decision.Source != "heuristic" || got.Decision.Complexity != "low" {
		t.Fatalf("decision=%+v", got.Decision)
	}
	if got.Engine != KindOllama || got.Output != "low-route" {
		t.Fatalf("engine=%q output=%q", got.Engine, got.Output)
	}
}

func TestChooseClassifierHigh(t *testing.T) {
	stub := func(_ context.Context, _ Request) (string, string, error) { return "high", "stubbed", nil }
	reg := newReg(t, Config{
		ClaudeCLI: ClaudeCLIConfig{Run: fakeCLI("high-route")},
		Choose:    ChooseConfig{Classify: stub},
	})

	// Default policy is cli-first: a "high" estimate routes to claude-cli (not api), and
	// with no Utilization configured there is no subscription spill.
	got := mustRoute(t, reg, KindChoose, Request{Prompt: "do something hard"})
	if got.Decision == nil || got.Decision.Source != "ollama-classifier" || got.Decision.Complexity != "high" {
		t.Fatalf("decision=%+v", got.Decision)
	}
	if got.Engine != KindClaudeCLI || got.Output != "high-route" {
		t.Fatalf("engine=%q output=%q", got.Engine, got.Output)
	}
	if got.Decision.Spilled {
		t.Fatalf("unexpected spill: %+v", got.Decision)
	}
}

// When subscription utilization is at/above the spill threshold, an estimated claude-cli
// pick spills to claude-api to protect dev headroom; below it, choose stays on claude-cli.
func TestChooseSpillsAtHighUsage(t *testing.T) {
	an := anthropicStub("api-spill")
	defer an.Close()
	stub := func(_ context.Context, _ Request) (string, string, error) { return "high", "hard", nil }
	build := func(frac float64) *prizm.Registry {
		return newReg(t, Config{
			ClaudeCLI: ClaudeCLIConfig{Run: fakeCLI("cli-answer")},
			ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"},
			Choose: ChooseConfig{
				Classify:    stub,
				Utilization: func() (float64, bool) { return frac, true },
			},
		})
	}

	// High usage => spill to api.
	hi := mustRoute(t, build(0.95), KindChoose, Request{Prompt: "hard task"})
	if hi.Engine != KindClaudeAPI || hi.Output != "api-spill" {
		t.Fatalf("high usage: engine=%q output=%q", hi.Engine, hi.Output)
	}
	if hi.Decision == nil || !hi.Decision.Spilled || hi.Decision.CLIUsage != 0.95 {
		t.Fatalf("high usage decision=%+v", hi.Decision)
	}

	// Low usage => stay on cli, no spill.
	lo := mustRoute(t, build(0.10), KindChoose, Request{Prompt: "hard task"})
	if lo.Engine != KindClaudeCLI || lo.Output != "cli-answer" {
		t.Fatalf("low usage: engine=%q output=%q", lo.Engine, lo.Output)
	}
	if lo.Decision == nil || lo.Decision.Spilled {
		t.Fatalf("low usage should not spill: %+v", lo.Decision)
	}
}

// Picked leaf unavailable => router walks the availability-fallback chain.
func TestChooseAvailabilityFallback(t *testing.T) {
	reg := newReg(t, Config{
		Ollama:    OllamaConfig{BaseURL: "http://127.0.0.1:1", Model: "m"}, // unreachable => unavailable
		ClaudeCLI: ClaudeCLIConfig{Run: fakeCLI("fallback-cli")},
	})

	got := mustRoute(t, reg, KindChoose, Request{Prompt: "hi"}) // low => ollama first
	if got.Engine != KindClaudeCLI || got.Output != "fallback-cli" {
		t.Fatalf("engine=%q output=%q", got.Engine, got.Output)
	}
	if got.Decision == nil || !got.Decision.Fallback {
		t.Fatalf("expected fallback, decision=%+v", got.Decision)
	}
}

func TestUnavailableAndInvalid(t *testing.T) {
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{APIKey: ""}}) // key unset

	if _, err := route(reg, KindClaudeAPI, Request{Prompt: ""}); !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("empty prompt: want ErrInvalidRequest, got %v", err)
	}
	if _, err := route(reg, KindClaudeAPI, Request{Prompt: "x"}); !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("missing key: want ErrProcessorUnavailable, got %v", err)
	}
}

// The router needs the spawner capability; without it, it must say so clearly.
func TestChooseRequiresSpawner(t *testing.T) {
	reg := prizm.NewRegistry(0)
	// Register choose WITHOUT WithSpawner — env.Spawn will be nil.
	if err := reg.Register(KindChoose, prizm.NewPrizm(NewChoose(ChooseConfig{}), graveyard.NewMemory())); err != nil {
		t.Fatal(err)
	}
	if _, err := route(reg, KindChoose, Request{Prompt: "hi", Choose: &ChooseOptions{Force: KindOllama}}); !errors.Is(err, prizm.ErrNoSpawner) {
		t.Fatalf("want ErrNoSpawner, got %v", err)
	}
}

// OLLAMA_HOST conventionally carries a bare host:port; the client must default the scheme
// so the URL is dialable (a scheme-less base yields "missing protocol scheme").
func TestOllamaBaseURLSchemeNormalization(t *testing.T) {
	if c := newOllamaClient(OllamaConfig{BaseURL: "127.0.0.1:11434"}); c.base != "http://127.0.0.1:11434" {
		t.Fatalf("bare host:port not normalized: %q", c.base)
	}
	if c := newOllamaClient(OllamaConfig{BaseURL: "https://host:443"}); c.base != "https://host:443" {
		t.Fatalf("already-qualified base altered: %q", c.base)
	}
	if c := newOllamaClient(OllamaConfig{}); c.base != "http://localhost:11434" {
		t.Fatalf("empty base default changed: %q", c.base)
	}
}

// The classifier must send ollama's structured-output `format` schema so a weak local
// model is constrained to {complexity,reason} instead of answering the task.
func TestOllamaClassifierStructuredOutput(t *testing.T) {
	var gotFormat any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotFormat = body["format"]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": `{"complexity":"high","reason":"deep task"}`},
		})
	}))
	defer srv.Close()

	classify := OllamaClassifier(OllamaConfig{BaseURL: srv.URL, Model: "m"}, "")
	cx, reason, err := classify(context.Background(), Request{Prompt: "prove a theorem"})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if cx != "high" || reason != "deep task" {
		t.Fatalf("parsed complexity=%q reason=%q", cx, reason)
	}
	schema, ok := gotFormat.(map[string]any)
	if !ok {
		t.Fatalf("no structured-output `format` schema sent: %v", gotFormat)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil || props["complexity"] == nil {
		t.Fatalf("format schema missing complexity constraint: %v", gotFormat)
	}
}

// anthropicStubCapture is anthropicStub plus a hook that records the decoded request body,
// so a test can assert what the claude-api leaf actually sent (e.g. output_config.effort).
func anthropicStubCapture(text string, gotBody *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"usage":   map[string]int{"input_tokens": 7, "output_tokens": 11},
		})
	}))
}

func sentEffort(t *testing.T, body map[string]any) any {
	t.Helper()
	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		return nil
	}
	return oc["effort"]
}

// claude-api carries Request.Claude.Effort through to the Anthropic body as
// output_config.effort, and echoes the applied effort back on the Result.
func TestClaudeAPIEffortPassthrough(t *testing.T) {
	var body map[string]any
	an := anthropicStubCapture("ok", &body)
	defer an.Close()
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"}})

	got := mustRoute(t, reg, KindClaudeAPI, Request{Prompt: "hi", Claude: &ClaudeOptions{Effort: "high"}})
	if e := sentEffort(t, body); e != "high" {
		t.Fatalf("output_config.effort sent = %v, want \"high\"", e)
	}
	if got.Effort != "high" {
		t.Fatalf("Result.Effort = %q, want \"high\"", got.Effort)
	}

	// Absent effort => no output_config at all (don't constrain the model unasked).
	body = nil
	got = mustRoute(t, reg, KindClaudeAPI, Request{Prompt: "hi"})
	if _, present := body["output_config"]; present {
		t.Fatalf("output_config present without effort: %v", body["output_config"])
	}
	if got.Effort != "" {
		t.Fatalf("Result.Effort = %q, want empty", got.Effort)
	}
}

// An unrecognized effort is bad input, not unavailability => prizm.ErrInvalidRequest,
// and it's rejected before any network call.
func TestClaudeAPIBadEffort(t *testing.T) {
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{APIKey: "k"}}) // no BaseURL: must not be reached
	_, err := route(reg, KindClaudeAPI, Request{Prompt: "hi", Claude: &ClaudeOptions{Effort: "bogus"}})
	if !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("bad effort: want ErrInvalidRequest, got %v", err)
	}
}

// The choose router forwards the SAME Request verbatim, so Claude.Effort reaches the
// picked leaf without choose knowing about it.
func TestChooseForwardsEffort(t *testing.T) {
	var body map[string]any
	an := anthropicStubCapture("forced", &body)
	defer an.Close()
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"}})

	got := mustRoute(t, reg, KindChoose, Request{
		Prompt: "anything",
		Claude: &ClaudeOptions{Effort: "medium"},
		Choose: &ChooseOptions{Force: KindClaudeAPI},
	})
	if got.Engine != KindClaudeAPI {
		t.Fatalf("engine=%q want claude-api", got.Engine)
	}
	if e := sentEffort(t, body); e != "medium" {
		t.Fatalf("effort did not propagate through choose: output_config.effort = %v", e)
	}
}

// KeyFunc supplies the API key at request time (the runtime-managed key path), overriding the
// static APIKey and reaching the x-api-key header.
func TestClaudeAPIKeyFuncSuppliesKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()

	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{
		BaseURL: srv.URL,
		KeyFunc: func(string) (string, bool) { return "sk-ant-runtime-key", true },
	}})
	out := mustRoute(t, reg, KindClaudeAPI, Request{Prompt: "hi"})
	if out.Output != "ok" {
		t.Fatalf("output = %q", out.Output)
	}
	if gotKey != "sk-ant-runtime-key" {
		t.Fatalf("x-api-key = %q, want the runtime key", gotKey)
	}
}

// With no model configured, the ollama leaf auto-detects the first locally-pulled model from
// /api/tags (zero-config) and uses it for the chat call.
func TestOllamaAutoDetectsModel(t *testing.T) {
	var chatModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen2.5:0.5b"}}})
		case "/api/chat":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			chatModel = body.Model
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "auto-hi"}, "prompt_eval_count": 1, "eval_count": 1})
		}
	}))
	defer srv.Close()

	reg := newReg(t, Config{Ollama: OllamaConfig{BaseURL: srv.URL}}) // no Model => auto-detect
	out := mustRoute(t, reg, KindOllama, Request{Prompt: "hi"})
	if out.Output != "auto-hi" {
		t.Fatalf("output=%q", out.Output)
	}
	if chatModel != "qwen2.5:0.5b" {
		t.Fatalf("chat used model %q, want auto-detected qwen2.5:0.5b", chatModel)
	}
}

// An ollama that errors (non-200) is treated as UNAVAILABLE, so choose falls back to a working
// engine (claude-api) instead of surfacing a hard 502. This is the path the Files "Ask AI"
// action relies on when ollama is misconfigured.
func TestChooseFallsBackWhenOllamaErrors(t *testing.T) {
	ollamaBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "m"}}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // chat fails
	}))
	defer ollamaBad.Close()
	an := anthropicStub("api-fallback")
	defer an.Close()

	reg := newReg(t, Config{
		Ollama: OllamaConfig{BaseURL: ollamaBad.URL},
		ClaudeCLI: ClaudeCLIConfig{Run: func(context.Context, string, []string, string, []string) ([]byte, error) {
			return nil, errors.New("no cli")
		}},
		ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"},
		// No classifier => heuristic; short prompt => low => ollama is tried first, then the
		// chain skips the (unavailable) cli and reaches claude-api.
	})
	got := mustRoute(t, reg, KindChoose, Request{Prompt: "hi"})
	if got.Engine != KindClaudeAPI || got.Output != "api-fallback" {
		t.Fatalf("expected fallback to claude-api, got engine=%q output=%q", got.Engine, got.Output)
	}
	if got.Decision == nil || !got.Decision.Fallback {
		t.Fatalf("expected decision.fallback=true, got %+v", got.Decision)
	}
}

// Inline image/PDF files become image/document content blocks (before the text block); other
// media is named in the prompt by the assembler but not sent as a block.
func TestClaudeAPIMultimodal(t *testing.T) {
	var body map[string]any
	an := anthropicStubCapture("ok", &body)
	defer an.Close()
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{BaseURL: an.URL, APIKey: "k"}})
	mustRoute(t, reg, KindClaudeAPI, Request{
		Prompt: "describe",
		Inline: []InlineFile{
			{Path: "me/a.txt", Content: "hello text"},
			{Path: "me/img.png", Content: "BASE64IMG", MediaType: "image/png"},
			{Path: "me/doc.pdf", Content: "BASE64PDF", MediaType: "application/pdf"},
		},
	})
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages in body: %v", body)
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content is not a multimodal array: %T", msgs[0].(map[string]any)["content"])
	}
	types := map[string]int{}
	for _, b := range content {
		types[b.(map[string]any)["type"].(string)]++
	}
	if types["image"] != 1 || types["document"] != 1 || types["text"] != 1 {
		t.Fatalf("block types = %v, want one each of image/document/text", types)
	}
	// The text block carries the prompt + the text file content (img/pdf only as blocks).
	last := content[len(content)-1].(map[string]any)
	if txt, _ := last["text"].(string); !strings.Contains(txt, "hello text") || !strings.Contains(txt, "describe") {
		t.Fatalf("text block missing prompt/text-file content: %q", txt)
	}
}

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// claude-cli runs PER USER: a linked user's token + isolated CLAUDE_CONFIG_DIR are injected; an
// unlinked user is unavailable; and a config-dir failure fails CLOSED (never the shared dir).
func TestClaudeCLIPerUser(t *testing.T) {
	var gotEnv []string
	capRun := func(_ context.Context, _ string, _ []string, _ string, extraEnv []string) ([]byte, error) {
		gotEnv = extraEnv
		return json.Marshal(map[string]any{"type": "result", "result": "ok", "is_error": false, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}
	linked := func(string) (string, bool) { return "claude_token_X", true }
	cfgOK := func(string) (string, error) { return "/tmp/cfg", nil }

	reg := newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: capRun, TokenFunc: linked, ConfigDirFunc: cfgOK}})
	if out := mustRoute(t, reg, KindClaudeCLI, Request{Prompt: "hi"}); out.Output != "ok" {
		t.Fatalf("output=%q", out.Output)
	}
	if !envHas(gotEnv, "CLAUDE_CODE_OAUTH_TOKEN=claude_token_X") || !envHas(gotEnv, "CLAUDE_CONFIG_DIR=/tmp/cfg") {
		t.Fatalf("per-user env not injected: %v", gotEnv)
	}

	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: capRun, TokenFunc: func(string) (string, bool) { return "", false }, ConfigDirFunc: cfgOK}})
	if _, err := route(reg, KindClaudeCLI, Request{Prompt: "hi"}); !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("unlinked: err=%v want unavailable", err)
	}

	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: capRun, TokenFunc: linked, ConfigDirFunc: func(string) (string, error) { return "", errors.New("mkdir fail") }}})
	if _, err := route(reg, KindClaudeCLI, Request{Prompt: "hi"}); !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("configdir fail: err=%v want unavailable (fail closed)", err)
	}
}

// claude-cli forwards reasoning effort as the CLI's --effort flag; an unknown level is a 400
// (caught before exec), and the applied level is echoed back on the Result.
func TestClaudeCLIEffort(t *testing.T) {
	var gotArgs []string
	capRun := func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		gotArgs = args
		return json.Marshal(map[string]any{"type": "result", "result": "ok", "is_error": false, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}

	reg := newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: capRun}})
	out := mustRoute(t, reg, KindClaudeCLI, Request{Prompt: "hi", Claude: &ClaudeOptions{Effort: "high"}})
	if out.Effort != "high" {
		t.Fatalf("result effort=%q want high", out.Effort)
	}
	if !argPair(gotArgs, "--effort", "high") {
		t.Fatalf("--effort high not passed: %v", gotArgs)
	}

	reg = newReg(t, Config{ClaudeCLI: ClaudeCLIConfig{Run: capRun}})
	if _, err := route(reg, KindClaudeCLI, Request{Prompt: "hi", Claude: &ClaudeOptions{Effort: "bogus"}}); !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("bad effort: err=%v want ErrInvalidRequest", err)
	}
}

// argPair reports whether args contains the consecutive pair flag, val.
func argPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// With no static key and KeyFunc reporting none, the leaf is unavailable (not a crash) — so
// choose's availability fallback still applies.
func TestClaudeAPIUnavailableWithoutKey(t *testing.T) {
	reg := newReg(t, Config{ClaudeAPI: ClaudeAPIConfig{
		KeyFunc: func(string) (string, bool) { return "", false },
	}})
	if _, err := route(reg, KindClaudeAPI, Request{Prompt: "hi"}); !errors.Is(err, ErrProcessorUnavailable) {
		t.Fatalf("err = %v, want ErrProcessorUnavailable", err)
	}
}
