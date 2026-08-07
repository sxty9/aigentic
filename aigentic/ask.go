package aigentic

import (
	"encoding/json"
	"regexp"
	"strings"
)

// This file is the ONE definition of aigentic's structured-question protocol: the shape a model
// emits when it wants the user to make a choice whose answers are clearly enumerable, plus the
// server-side parse that lifts that shape out of the model's text into a typed Result.Ask. It is
// modeled on Claude Code's AskUserQuestion tool, but rides the existing stateless-transcript model
// (no native tool-calling), so it works identically across all three leaves — ollama, claude-api
// and claude-cli — and needs no per-engine variant. The router forwards Request verbatim and
// returns the leaf's Result, so Ask propagates through `choose` for free.
//
// Opt-in: a leaf injects the instruction and runs the parse ONLY when Request.Interactive is set,
// so a one-shot Ask-AI turn (Files) or an MCP flow (hosuto) is byte-for-byte unchanged.

// Ask is a structured question a model posed to the user, to be rendered as clickable options in
// the chat bubble. It carries one or more questions (Claude Code allows a small batch).
type Ask struct {
	Questions []AskQuestion `json:"questions"`
}

// AskQuestion is one question within an Ask: a short header, the question text, the offered
// options, and whether several may be chosen together.
type AskQuestion struct {
	Header      string      `json:"header,omitempty"` // 2–3 word topic label (a chip in the UI)
	Question    string      `json:"question"`         // the question, in words
	Options     []AskOption `json:"options"`          // the enumerated choices
	MultiSelect bool        `json:"multiSelect,omitempty"`
}

// AskOption is one offered choice: a short label plus an optional one-line description.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// valid reports whether an Ask is well-formed enough to render: at least one question, each with a
// non-empty prompt and at least one option carrying a non-empty label. A malformed block is left
// as plain text (self-healing — a weak model that botches the shape never breaks the chat).
func (a Ask) valid() bool {
	if len(a.Questions) == 0 {
		return false
	}
	for _, q := range a.Questions {
		if strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 {
			return false
		}
		hasLabel := false
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) != "" {
				hasLabel = true
				break
			}
		}
		if !hasLabel {
			return false
		}
	}
	return true
}

// askInstruction is appended to the SYSTEM prompt on an Interactive run. It is a format spec for
// the model, not user-facing text. Kept tight so it costs little on every interactive turn.
const askInstruction = "When the user's next step is a choice with a clear, finite set of answers, " +
	"you MAY ask it as a structured question instead of prose. To do so, end your reply with EXACTLY " +
	"ONE fenced block tagged `ask` whose body is JSON of this shape:\n\n" +
	"```ask\n" +
	`{"questions":[{"header":"<2-3 word topic>","question":"<the question>","multiSelect":false,"options":[{"label":"<short answer>","description":"<one line, optional>"}]}]}` +
	"\n```\n\n" +
	"Rules:\n" +
	"- Use it only when the answer set is genuinely clear and enumerable; otherwise answer normally in prose.\n" +
	"- Keep 2–4 options per question; keep labels short; give each a one-line description when it helps.\n" +
	"- Set \"multiSelect\":true only when several options may sensibly be chosen together.\n" +
	"- Also ask the question in prose BEFORE the block so the reply reads naturally; never mention the block or its format to the user.\n" +
	"- The user can always type a free-form answer, so never add an \"Other\" or \"Something else\" option.\n" +
	"- Emit at most one such block, as the very last thing in your reply."

// askSystem appends the structured-question instruction to a base system prompt when the request
// opted into Interactive; otherwise it returns base unchanged. A "" base yields just the
// instruction (claude-cli passes in.System, which may be empty).
func askSystem(base string, in Request) string {
	if !in.Interactive {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return askInstruction
	}
	return base + "\n\n" + askInstruction
}

// askBlockRE matches a fenced ```ask block and captures its JSON body. Non-greedy to the first
// closing fence; the JSON payload never contains a fence, so this is unambiguous.
var askBlockRE = regexp.MustCompile("(?s)```[ \t]*ask[ \t]*\r?\n(.*?)```")

// parseAsk lifts the first well-formed ```ask block out of a model answer. It returns the answer
// with that block removed (trimmed) and the parsed Ask. When the request is not Interactive, or no
// valid block is present, it returns the text unchanged and a nil Ask — so non-interactive callers
// and malformed output are entirely unaffected.
func parseAsk(text string, in Request) (string, *Ask) {
	if !in.Interactive {
		return text, nil
	}
	loc := askBlockRE.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, nil
	}
	body := strings.TrimSpace(text[loc[2]:loc[3]])
	var a Ask
	if err := json.Unmarshal([]byte(body), &a); err != nil || !a.valid() {
		return text, nil // leave a malformed block as plain text
	}
	cleaned := strings.TrimSpace(text[:loc[0]] + text[loc[1]:])
	return cleaned, &a
}

// withAsk finalizes a leaf's Result: it sets Output to the model's raw answer with any structured
// question block stripped, and Ask to the parsed question (nil on a non-interactive run). Shared by
// all three leaves so the protocol lives in exactly one place.
func withAsk(base Result, raw string, in Request) Result {
	base.Output, base.Ask = parseAsk(raw, in)
	return base
}

// finalize builds a leaf's Result from the raw model answer: it strips the structured-question
// block (withAsk) and then names, IN THE ANSWER, any image or PDF attachment the running engine
// could not read (canImage/canPDF describe what THIS engine perceives). The gap belongs in the
// answer the user reads, not only in the provenance items — a request may carry an attachment the
// running model cannot see, and an unnamed gap invites the model's text to fill it with invention.
func finalize(base Result, raw string, in Request, canImage, canPDF bool) Result {
	base = withAsk(base, raw, in)
	base.Output = noteMediaGap(base.Output, unreadMedia(in, canImage, canPDF))
	return base
}

// unreadMedia returns the display paths of image/PDF attachments the engine did NOT feed to the
// model as perceivable content, given what it can perceive (canImage/canPDF). Text is read by every
// engine; other binary types are name-only by design and are not reported here. It is how a leaf
// tells the user which visual/document attachments went unseen.
func unreadMedia(in Request, canImage, canPDF bool) []string {
	var out []string
	for _, f := range in.Inline {
		switch {
		case f.isImage() && !canImage:
			out = append(out, f.Path)
		case f.MediaType == "application/pdf" && !canPDF:
			out = append(out, f.Path)
		}
	}
	return out
}

// noteMediaGap appends a named, unmissable note to an answer for each attachment the model could not
// read, so the gap is part of the answer itself. No gap => the answer is returned unchanged.
func noteMediaGap(output string, unread []string) string {
	if len(unread) == 0 {
		return output
	}
	note := "⚠️ This model could not read the following attachment(s), so they were not considered: " +
		strings.Join(unread, ", ") + "."
	if strings.TrimSpace(output) == "" {
		return note
	}
	return output + "\n\n" + note
}
