package aigentic

import (
	"strings"
	"testing"
)

// a well-formed reply: prose, then a single ```ask block with one question and two options.
const askReply = "Sure — which database should we use?\n\n" +
	"```ask\n" +
	`{"questions":[{"header":"Database","question":"Which database?","options":[{"label":"PostgreSQL","description":"Relational"},{"label":"SQLite","description":"Embedded"}]}]}` +
	"\n```"

func TestParseAskExtractsBlock(t *testing.T) {
	clean, ask := parseAsk(askReply, Request{Interactive: true})
	if ask == nil {
		t.Fatal("expected an Ask, got nil")
	}
	if clean != "Sure — which database should we use?" {
		t.Errorf("cleaned prose = %q; want the question stripped of the block", clean)
	}
	if len(ask.Questions) != 1 || len(ask.Questions[0].Options) != 2 {
		t.Fatalf("ask = %+v; want 1 question / 2 options", ask)
	}
	if ask.Questions[0].Header != "Database" || ask.Questions[0].Options[0].Label != "PostgreSQL" {
		t.Errorf("ask fields not parsed: %+v", ask.Questions[0])
	}
}

func TestParseAskDisabledWhenNotInteractive(t *testing.T) {
	clean, ask := parseAsk(askReply, Request{}) // Interactive not set
	if ask != nil {
		t.Errorf("non-interactive request must never yield an Ask, got %+v", ask)
	}
	if clean != askReply {
		t.Errorf("non-interactive text must be returned verbatim (block NOT stripped)")
	}
}

func TestParseAskMalformedStaysText(t *testing.T) {
	bad := "hello\n\n```ask\n{not valid json}\n```"
	clean, ask := parseAsk(bad, Request{Interactive: true})
	if ask != nil {
		t.Errorf("malformed block must not parse into an Ask, got %+v", ask)
	}
	if clean != bad {
		t.Errorf("malformed block must be left as plain text")
	}
}

func TestParseAskRejectsEmptyOrOptionless(t *testing.T) {
	for _, body := range []string{
		`{"questions":[]}`,
		`{"questions":[{"question":"Q","options":[]}]}`,
		`{"questions":[{"question":"","options":[{"label":"A"}]}]}`,
		`{"questions":[{"question":"Q","options":[{"label":""}]}]}`,
	} {
		text := "p\n\n```ask\n" + body + "\n```"
		if _, ask := parseAsk(text, Request{Interactive: true}); ask != nil {
			t.Errorf("invalid ask %q parsed as valid", body)
		}
	}
}

func TestAskSystemAppendsOnlyWhenInteractive(t *testing.T) {
	if got := askSystem(defaultSystem, Request{}); got != defaultSystem {
		t.Errorf("non-interactive must leave the system prompt unchanged")
	}
	got := askSystem(defaultSystem, Request{Interactive: true})
	if !strings.Contains(got, defaultSystem) || !strings.Contains(got, "```ask") {
		t.Errorf("interactive system prompt must carry both the base and the ask instruction")
	}
	// An empty base (claude-cli with no in.System) yields just the instruction, no stray blank lines.
	if got := askSystem("", Request{Interactive: true}); got != askInstruction {
		t.Errorf("empty base must yield exactly the instruction, got %q", got)
	}
}

// TestInteractiveFlowSurfacesAsk proves the protocol rides an actual leaf: an engine that returns a
// question block yields Result.Ask with the block stripped from Output — and the SAME engine output
// on a non-interactive request surfaces no Ask and keeps the block in the text.
func TestInteractiveFlowSurfacesAsk(t *testing.T) {
	ol := ollamaStub(askReply)
	defer ol.Close()
	reg := newReg(t, Config{Ollama: OllamaConfig{BaseURL: ol.URL, Model: "m"}})

	got := mustRoute(t, reg, KindOllama, Request{Prompt: "hi", Interactive: true})
	if got.Ask == nil || len(got.Ask.Questions) != 1 {
		t.Fatalf("interactive run: want Result.Ask with 1 question, got %+v", got.Ask)
	}
	if strings.Contains(got.Output, "```ask") {
		t.Errorf("interactive run: the ask block must be stripped from Output, got %q", got.Output)
	}

	plain := mustRoute(t, reg, KindOllama, Request{Prompt: "hi"})
	if plain.Ask != nil {
		t.Errorf("non-interactive run must not surface an Ask")
	}
	if !strings.Contains(plain.Output, "```ask") {
		t.Errorf("non-interactive run must leave the block in Output verbatim")
	}
}

// TestInteractiveSurvivesChooseRouter guards the DEFAULT engine: `choose` forwards the Request
// verbatim and returns the leaf's Result (only stamping Decision), so a question posed by the
// delegated leaf must reach the caller through the router.
func TestInteractiveSurvivesChooseRouter(t *testing.T) {
	ol := ollamaStub(askReply)
	defer ol.Close()
	reg := newReg(t, Config{Ollama: OllamaConfig{BaseURL: ol.URL, Model: "m"}})

	got := mustRoute(t, reg, KindChoose, Request{Prompt: "hi", Interactive: true, Choose: &ChooseOptions{Force: KindOllama}})
	if got.Ask == nil || len(got.Ask.Questions) != 1 {
		t.Fatalf("choose must preserve the leaf's Ask (it is the default engine), got %+v", got.Ask)
	}
	if got.Decision == nil || got.Decision.Picked != KindOllama {
		t.Errorf("choose must still stamp its Decision, got %+v", got.Decision)
	}
	if strings.Contains(got.Output, "```ask") {
		t.Errorf("choose must carry the cleaned Output (block stripped), got %q", got.Output)
	}
}
