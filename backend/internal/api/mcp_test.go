package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// rpcEnvelope is the JSON-RPC 2.0 response shape the MCP transport returns.
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// toolResult is the shape of a tools/call result.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// mcpCall POSTs one JSON-RPC message to the MCP endpoint and returns the decoded envelope.
func mcpCall(t *testing.T, s *Server, access, method string, params any) rpcEnvelope {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, "POST", base+"mcp", body, access, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d %s", method, rec.Code, rec.Body)
	}
	var env rpcEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s: decode envelope: %v (%s)", method, err, rec.Body)
	}
	return env
}

func decodeToolResult(t *testing.T, raw json.RawMessage) toolResult {
	t.Helper()
	var tr toolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("decode tool result: %v (%s)", err, raw)
	}
	return tr
}

// initialize needs no auth (the handshake) and reports the server identity.
func TestMCPInitializeNoAuth(t *testing.T) {
	s := newServer(t, "sudo", "")
	env := mcpCall(t, s, "", "initialize", nil)
	if env.Error != nil {
		t.Fatalf("initialize errored: %+v", env.Error)
	}
	var res struct {
		ServerInfo struct{ Name string } `json:"serverInfo"`
	}
	_ = json.Unmarshal(env.Result, &res)
	if res.ServerInfo.Name != service {
		t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, service)
	}
}

// tools/list is unauthorized without a session, and lists aigentic.ask with one.
func TestMCPToolsListAuth(t *testing.T) {
	username, group := currentUser(t)
	s := newServer(t, group, "")
	access := mintAccess(t, username)

	// No cookie => JSON-RPC unauthorized.
	if env := mcpCall(t, s, "", "tools/list", nil); env.Error == nil || env.Error.Code != -32001 {
		t.Fatalf("tools/list without auth: env=%+v, want error -32001", env)
	}

	// With a session => the tool is listed.
	env := mcpCall(t, s, access, "tools/list", nil)
	if env.Error != nil {
		t.Fatalf("tools/list: %+v", env.Error)
	}
	var res struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	_ = json.Unmarshal(env.Result, &res)
	if len(res.Tools) != 1 || res.Tools[0].Name != "aigentic.ask" {
		t.Errorf("tools = %+v, want [aigentic.ask]", res.Tools)
	}
}

// A run-right holder can call aigentic.ask; it routes through the same registry as REST /run.
func TestMCPAskHappyPath(t *testing.T) {
	username, group := currentUser(t)
	ol := ollamaStub(t)
	defer ol.Close()
	s := newServer(t, group, ol.URL) // admin => holds every right
	access := mintAccess(t, username)

	env := mcpCall(t, s, access, "tools/call", map[string]any{
		"name":      "aigentic.ask",
		"arguments": map[string]any{"prompt": "hi", "engine": "ollama"},
	})
	if env.Error != nil {
		t.Fatalf("tools/call errored at transport: %+v", env.Error)
	}
	tr := decodeToolResult(t, env.Result)
	if tr.IsError {
		t.Fatalf("ask returned a tool error: %s", tr.Content[0].Text)
	}
	var payload struct {
		Output string `json:"output"`
		Engine string `json:"engine"`
	}
	_ = json.Unmarshal([]byte(tr.Content[0].Text), &payload)
	if payload.Output != "stub-hi" || payload.Engine != "ollama" {
		t.Errorf("ask payload = %+v, want output=stub-hi engine=ollama", payload)
	}
}

// Without the run right the tool refuses (a tool error, so the connection survives).
func TestMCPAskDeniedWithoutRunRight(t *testing.T) {
	username, _ := currentUser(t)
	s := newServer(t, "hp_aigentic_nonexistent_admin", "") // not admin, lacks the run right
	access := mintAccess(t, username)

	env := mcpCall(t, s, access, "tools/call", map[string]any{
		"name":      "aigentic.ask",
		"arguments": map[string]any{"prompt": "hi"},
	})
	if env.Error != nil {
		t.Fatalf("expected a tool error result, got transport error: %+v", env.Error)
	}
	tr := decodeToolResult(t, env.Result)
	if !tr.IsError {
		t.Fatalf("expected isError for a caller without the run right")
	}
}

// An admin passes the paid-API gate but the engine is unavailable (no key configured) — surfaced as
// a readable tool error, proving the gate lets the call through to the engine.
func TestMCPAskPaidEngineUnavailable(t *testing.T) {
	username, group := currentUser(t)
	s := newServer(t, group, "") // admin => holds the api right
	access := mintAccess(t, username)

	env := mcpCall(t, s, access, "tools/call", map[string]any{
		"name":      "aigentic.ask",
		"arguments": map[string]any{"prompt": "hi", "engine": "claude-api"},
	})
	if env.Error != nil {
		t.Fatalf("transport error: %+v", env.Error)
	}
	tr := decodeToolResult(t, env.Result)
	if !tr.IsError || tr.Content[0].Text != "the selected engine is unavailable" {
		t.Fatalf("want unavailable tool error, got isError=%v text=%q", tr.IsError, tr.Content[0].Text)
	}
}

// An unknown engine is refused before routing.
func TestMCPAskUnknownEngine(t *testing.T) {
	username, group := currentUser(t)
	s := newServer(t, group, "")
	access := mintAccess(t, username)

	env := mcpCall(t, s, access, "tools/call", map[string]any{
		"name":      "aigentic.ask",
		"arguments": map[string]any{"prompt": "hi", "engine": "gpt"},
	})
	tr := decodeToolResult(t, env.Result)
	if !tr.IsError {
		t.Fatalf("unknown engine should be a tool error")
	}
}
