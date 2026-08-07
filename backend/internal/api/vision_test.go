package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/prizm/prizm"
)

// blindOllamaStub answers /api/show with a text-only capability set and /api/chat with a canned
// reply — a local model that CANNOT see images.
func blindOllamaStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]string{"content": "stub"}, "prompt_eval_count": 1, "eval_count": 1,
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunImageToBlindModelRefused: posting an image request pinned to a text-only local model yields
// the named vision refusal (422), never a 200 with a fabricated description of the image.
func TestRunImageToBlindModelRefused(t *testing.T) {
	username, group := currentUser(t)
	ol := blindOllamaStub(t)
	s := newServer(t, group, ol.URL) // admin (primary group) => holds every right, so rights don't mask this
	access := mintAccess(t, username)

	req := aigentic.Request{
		Prompt: "Which devices are used here?",
		Inline: []aigentic.InlineFile{{Path: "room/photo.png", Content: base64.StdEncoding.EncodeToString([]byte("PNGDATA")), MediaType: "image/png"}},
	}
	rec := do(t, s, "POST", base+"run", runBody(t, aigentic.KindOllama, req), access, "csrf")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("image to blind model: got %d want 422; body=%s", rec.Code, rec.Body.String())
	}
}

// TestPaidKindGatesRouterAndAPI documents the cost-path invariant behind requirement 5: the choose
// router is gated by the SAME 'cost:api' right as the paid Claude API, because it may route there. A
// subject lacking that right therefore cannot reach the router at all (a 403 at the shell, a named
// state) and never causes a silent fallback to a blind model. The free engines are not gated.
func TestPaidKindGatesRouterAndAPI(t *testing.T) {
	for _, k := range []prizm.Kind{aigentic.KindChoose, aigentic.KindClaudeAPI} {
		if !paidKind(k) {
			t.Errorf("paidKind(%s) = false, want true (the cost right must gate it)", k)
		}
	}
	for _, k := range []prizm.Kind{aigentic.KindOllama, aigentic.KindClaudeCLI} {
		if paidKind(k) {
			t.Errorf("paidKind(%s) = true, want false (a free engine must not need the cost right)", k)
		}
	}
}
