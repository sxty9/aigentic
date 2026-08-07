package aigentic

import (
	"context"
	"fmt"
	"strings"

	"github.com/sxty9/prizm/prizm"
	"github.com/sxty9/prizm/subprizm"
)

// This file is the ONE definition of aigentic's text-extraction capability: given one or more
// attached files, return the text they CONTAIN. It exists because reading the text out of a
// document is an AI-shaped job every document service needs (presentr's uploads, the Files app,
// hosuto), and the value that makes it AI rather than a parser is recognizing text that lives
// INSIDE images — equipment nameplates, connector labels, model/serial numbers, handwriting —
// which a plain PDF text-layer read never sees. Per "Reuse before Build"/"Keine ähnlichen
// Geschwister", that recognition lives HERE once, not re-implemented (with a re-invented prompt)
// in each caller.
//
// It is NOT a new engine. The extract processor owns the extraction instruction and then forwards
// the request VERBATIM through the existing `choose` router, so the image-vision precondition
// (an image only ever reaches an engine that can SEE it — choose.go) and the availability fallback
// are reused unchanged, not duplicated. The caller supplies files; aigentic supplies the prompt.
//
// What extract deliberately does NOT do: it does not store its output (aigentic is stateless — the
// calling document service keeps the extract beside its document), and it does not parse a PDF's
// embedded text layer. Reading an existing text layer is exact and costs no AI; it is a plain
// parsing step that belongs to the caller's upload pipeline (or a shared non-AI library), not to
// the AI service. The division of labour is therefore: the caller extracts a present text layer
// itself (cheap, deterministic) and calls extract only for images and text-layer-less (scanned)
// pages — which is exactly the input a later chunking pass would split.

// KindExtract is the text-extraction capability's kind. Like `choose`, it is a router, not a leaf:
// it MUST be registered WithSpawner(reg) so it can delegate to `choose`. Because that delegation may
// resolve to the paid Anthropic API (choose can), the HTTP/MCP shells gate it exactly as they gate
// `choose` (paidKind) — the in-process spawn cannot be re-gated, so the gate is on the kind.
const KindExtract prizm.Kind = "extract"

// extractSystem pins the engine to transcription only: no summary, no answer, no commentary. Shared
// by every engine the request may land on (the leaves read Request.System via askSystem/append).
const extractSystem = "You are aigentic's text-extraction engine. Your only job is to transcribe the text " +
	"present in the attached file(s) — never summarize it, answer questions about it, or add commentary."

// extractInstruction is the server-owned extraction prompt. It stresses the point of this
// capability: text that appears INSIDE images and photographs must be read too, not only a
// document's machine-readable text layer. Kept in ONE place so every document service gets the same
// high-quality transcription instead of hand-rolling its own.
const extractInstruction = "Transcribe every piece of text contained in the attached file(s), exactly as written. " +
	"Read text that appears INSIDE images and photographs too — equipment nameplates, connector and terminal " +
	"labels, model and serial numbers, handwriting, stamps, and captions — not only a document's machine-readable " +
	"text layer. Keep the natural reading order; for a multi-page document, transcribe the pages in order and " +
	"separate them clearly. Output ONLY the transcribed text — no preamble, no explanation, no Markdown code " +
	"fences. If a page or image genuinely holds no legible text, emit the line \"[no legible text]\" for it " +
	"instead of inventing content."

// extractPrompt composes the forwarded user turn: the server-owned instruction, plus the caller's
// optional focus (their Request.Prompt, e.g. "pay attention to the rating plate") appended as
// guidance rather than replacing the instruction.
func extractPrompt(focus string) string {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return extractInstruction
	}
	return extractInstruction + "\n\nThe caller asked you to pay particular attention to: " + focus
}

// appendExtractSystem folds the transcription-only guidance into any caller-supplied System prompt.
func appendExtractSystem(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return extractSystem
	}
	return base + "\n\n" + extractSystem
}

// NewExtract returns the extract router processor (Kind "extract"). It requires at least one
// attached file, composes the server-owned extraction prompt, and forwards the request to `choose`
// via the spawner — so the vision precondition, engine fallback, model reporting and usage
// accounting all come from the existing path. The returned Result's Output is the transcribed text;
// Engine/Model name which engine and model actually read the file(s) (satisfying the AI-answer
// labelling axiom), and any attachment the running engine could not read is already named in the
// answer by the leaf (finalize/noteMediaGap) — so a caller never mistakes an unread scan for an
// empty document.
func NewExtract() prizm.Processor {
	return prizm.NewTyped(func(ctx context.Context, in Request, env prizm.Env) (Result, error) {
		if len(in.Inline) == 0 {
			return Result{}, fmt.Errorf("%w: extract needs at least one attached file", prizm.ErrInvalidRequest)
		}
		if env.Spawn == nil {
			return Result{}, prizm.ErrNoSpawner
		}
		fwd := in
		fwd.Prompt = extractPrompt(in.Prompt)
		fwd.System = appendExtractSystem(in.System)
		fwd.OutputFormat = "text" // transcription is text; never coax the engine into JSON/markdown here
		fwd.Interactive = false   // extraction never asks the user a question
		fwd.MCP = nil             // no agentic tool use for a transcription turn
		return subprizm.SpawnTyped[Request, Result](ctx, env.Spawn, env.Header, KindChoose, fwd)
	})
}

// Extract is the P-layer entry point for the non-passthrough surfaces (the MCP door): it hands the
// typed Request to the registry under KindExtract and returns the typed Result, so the HTTP shell
// never encodes/decodes Data itself. It mirrors Route but enforces extract's own precondition — at
// least one attached file — instead of Route's non-empty-Prompt rule, because for extraction the
// prompt is server-owned and the caller's Prompt is optional focus. subject is server-authoritative.
func Extract(ctx context.Context, r Router, subject string, in Request) (Result, error) {
	if len(in.Inline) == 0 {
		return Result{}, fmt.Errorf("%w: extract needs at least one attached file", prizm.ErrInvalidRequest)
	}
	data, err := prizm.EncodeData(in)
	if err != nil {
		return Result{}, err
	}
	req := prizm.Request{Header: prizm.Header{Kind: KindExtract, Subject: subject}, Data: data}
	resp, err := r.Route(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return prizm.DecodeData[Result](resp.Data)
}
