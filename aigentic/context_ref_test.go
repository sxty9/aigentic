package aigentic

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sxty9/prizm/graveyard"
)

// An inline attachment carried as a graveyard Ref (stored on an earlier turn) must produce the
// EXACT same request the caller would build by re-sending the full Content — that is the whole
// point of the Ref: not retransmitting the bytes every turn. This checks the claude-api leaf's
// user-content block is byte-identical for a Content request and the matching Ref request.
func TestInlineRefMatchesContentBlock(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir()}
	b64 := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))

	// The caller stores exactly the bytes it would otherwise have put in Content.
	ref, err := grave.Put(context.Background(), "", []byte(b64))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	contentReq := Request{Prompt: "describe", Inline: []InlineFile{{Path: "room/photo.png", Content: b64, MediaType: "image/png"}}}
	refReq := Request{Prompt: "describe", Inline: []InlineFile{{Path: "room/photo.png", Ref: ref, MediaType: "image/png"}}}

	cPrompt, _, _, cResolved, err := assemble(context.Background(), envFor(grave, "nanu"), contentReq, lim)
	if err != nil {
		t.Fatalf("assemble content: %v", err)
	}
	rPrompt, _, _, rResolved, err := assemble(context.Background(), envFor(grave, "nanu"), refReq, lim)
	if err != nil {
		t.Fatalf("assemble ref: %v", err)
	}

	cBlock := claudeUserContent(contentReq, cPrompt, cResolved)
	rBlock := claudeUserContent(refReq, rPrompt, rResolved)
	if !reflect.DeepEqual(cBlock, rBlock) {
		t.Fatalf("ref block differs from content block:\n content=%#v\n ref=%#v", cBlock, rBlock)
	}
	// And the resolved base64 is the original payload, not empty.
	if got := rResolved["room/photo.png"]; got != b64 {
		t.Fatalf("resolved ref payload = %q, want %q", got, b64)
	}
}

// A Ref-carried text attachment is folded into the prompt exactly like the same Content would be.
func TestInlineRefTextMatchesContent(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir(), MaxContextBytes: DefaultMaxContextBytes}
	text := "# Spec\nhello from an earlier turn"
	ref, err := grave.Put(context.Background(), "", []byte(text))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	in := Request{Prompt: "summarize", Inline: []InlineFile{{Path: "me/spec.md", Ref: ref}}}

	prompt, items, _, _, err := assemble(context.Background(), envFor(grave, "nanu"), in, lim)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if want := "hello from an earlier turn"; !strings.Contains(prompt, want) {
		t.Fatalf("prompt missing ref-resolved text:\n%s", prompt)
	}
	// The provenance item keeps the caller's Ref (no re-Put of already-stored bytes).
	if it := itemFor(items, "spec.md"); it == nil || it.Ref != ref {
		t.Fatalf("ref item wrong: %+v", it)
	}
}

// An image attachment with NEITHER Content NOR Ref is a clear error — never a silent empty image
// block that would make the model answer about a picture it never received.
func TestInlineEmptyImageErrors(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir()}
	in := Request{Prompt: "describe", Inline: []InlineFile{{Path: "room/photo.png", MediaType: "image/png"}}}
	if _, _, _, _, err := assemble(context.Background(), envFor(grave, "nanu"), in, lim); err == nil {
		t.Fatal("expected an error for an image attachment with no content and no ref")
	}
}

// A Ref that names nothing in the graveyard is a hard fault, not a silent skip.
func TestInlineDanglingRefErrors(t *testing.T) {
	grave := graveyard.NewMemory()
	lim := Limits{ContextRoot: t.TempDir()}
	in := Request{Prompt: "describe", Inline: []InlineFile{{Path: "room/photo.png", Ref: graveyard.Ref("sha256:deadbeef"), MediaType: "image/png"}}}
	if _, _, _, _, err := assemble(context.Background(), envFor(grave, "nanu"), in, lim); err == nil {
		t.Fatal("expected an error for a dangling inline ref")
	}
}

// The claude-cli materialization must ALSO honour a Ref: the file written to disk for a Ref-only
// attachment is byte-identical to the one a Content attachment would produce.
func TestMaterializeCLIFilesResolvesRef(t *testing.T) {
	grave := graveyard.NewMemory()
	raw := []byte{0xff, 0xd8, 0xff, 0xe0} // JPEG magic
	b64 := base64.StdEncoding.EncodeToString(raw)
	ref, err := grave.Put(context.Background(), "", []byte(b64))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	in := Request{Inline: []InlineFile{{Path: "page6.jpg", Ref: ref, MediaType: "image/jpeg"}}}

	dir, _, items, err := materializeCLIFiles(context.Background(), envFor(grave, "u"), in)
	if err != nil {
		t.Fatalf("materializeCLIFiles: %v", err)
	}
	defer os.RemoveAll(dir)

	got, err := os.ReadFile(filepath.Join(dir, "page6.jpg"))
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if !reflect.DeepEqual(got, raw) {
		t.Fatalf("materialized bytes = %v, want the decoded ref payload %v", got, raw)
	}
	if len(items) != 1 || items[0].Path != "page6.jpg" || items[0].Bytes != len(raw) {
		t.Fatalf("provenance = %+v, want the caller's path with %d bytes", items, len(raw))
	}
}
