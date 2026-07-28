package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testRegistry() *Registry {
	r := NewRegistry("aigentic", "0.1.0")
	r.Register(Tool{
		Name:        "aigentic.echo",
		Description: "echo",
		Handler: func(_ context.Context, caller any, args json.RawMessage) (any, error) {
			return map[string]any{"caller": caller, "args": string(args)}, nil
		},
	})
	return r
}

// okAuth is an Authenticator that accepts everyone as a fixed caller.
func okAuth(*http.Request) (any, error) { return "u", nil }

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGETNotAllowed(t *testing.T) {
	h := testRegistry().Handler(okAuth)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d want 405", rec.Code)
	}
}

func TestNotificationGetsAccepted(t *testing.T) {
	h := testRegistry().Handler(okAuth)
	// No "id" => a notification => 202 and no body.
	rec := post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Errorf("notification: got %d want 202", rec.Code)
	}
}

func TestParseError(t *testing.T) {
	h := testRegistry().Handler(okAuth)
	rec := post(t, h, `{not json`)
	var env struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != -32700 {
		t.Errorf("parse error: env=%s want code -32700", rec.Body)
	}
}

func TestUnknownMethod(t *testing.T) {
	h := testRegistry().Handler(okAuth)
	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"frobnicate"}`)
	var env struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != -32601 {
		t.Errorf("unknown method: env=%s want code -32601", rec.Body)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	h := testRegistry().Handler(okAuth)
	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aigentic.nope"}}`)
	var env struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != -32602 {
		t.Errorf("unknown tool: env=%s want code -32602", rec.Body)
	}
}

func TestToolsListRequiresAuth(t *testing.T) {
	denied := func(*http.Request) (any, error) { return nil, errAuth }
	h := testRegistry().Handler(denied)
	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var env struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != -32001 {
		t.Errorf("unauth tools/list: env=%s want code -32001", rec.Body)
	}
}

var errAuth = &authErr{}

type authErr struct{}

func (*authErr) Error() string { return "denied" }
