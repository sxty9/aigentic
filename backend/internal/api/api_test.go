package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/auth"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

var secret = []byte("test-secret-do-not-use-in-prod")

func mintAccess(t *testing.T, sub string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "type": "access", "exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// currentUser returns the running account's username and primary group; the auth layer
// resolves groups live from the OS, so a test needs a real account.
func currentUser(t *testing.T) (username, primaryGroup string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("cannot resolve primary group: %v", err)
	}
	return u.Username, g.Name
}

// ollamaStub answers the leaf's /api/chat so kind=ollama returns 200.
func ollamaStub(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"content": "stub-hi"},
			"prompt_eval_count": 1, "eval_count": 1,
		})
	}))
}

func newServer(t *testing.T, adminGroup, ollamaURL string) *Server {
	t.Helper()
	reg := prizm.NewRegistry(0)
	if err := aigentic.Register(reg, graveyard.NewMemory(), aigentic.Config{
		Ollama: aigentic.OllamaConfig{BaseURL: ollamaURL},
	}); err != nil {
		t.Fatal(err)
	}
	store := secretstore.New(filepath.Join(t.TempDir(), "anthropic.key"), "")
	return New(auth.NewVerifier(secret, adminGroup), reg, store)
}

func do(t *testing.T, s *Server, method, path string, body []byte, access, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if access != "" {
		r.AddCookie(&http.Cookie{Name: "h_access", Value: access})
	}
	if csrf != "" {
		r.AddCookie(&http.Cookie{Name: "h_csrf", Value: csrf})
		r.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func runBody(t *testing.T, kind prizm.Kind, in aigentic.Request) []byte {
	t.Helper()
	data, err := prizm.EncodeData(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(prizm.Request{Header: prizm.Header{Kind: kind}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInfoRequiresAuth(t *testing.T) {
	s := newServer(t, "sudo", "")
	if rec := do(t, s, "GET", base+"info", nil, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: got %d want 401", rec.Code)
	}
}

func TestInfoAndRunHappyPath(t *testing.T) {
	username, group := currentUser(t)
	ol := ollamaStub(t)
	defer ol.Close()
	s := newServer(t, group, ol.URL) // admin = primary group => isAdmin, holds every right
	access := mintAccess(t, username)
	const csrf = "csrf-token"

	// info lists the four kinds.
	rec := do(t, s, "GET", base+"info", nil, access, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("info: %d %s", rec.Code, rec.Body)
	}
	var info struct {
		Kinds []string `json:"kinds"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if len(info.Kinds) != 4 {
		t.Errorf("info kinds=%v", info.Kinds)
	}

	// run kind=ollama routes to the stub.
	rec = do(t, s, "POST", base+"run", runBody(t, aigentic.KindOllama, aigentic.Request{Prompt: "hi"}), access, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	var resp prizm.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out, _ := prizm.DecodeData[aigentic.Result](resp.Data)
	if out.Output != "stub-hi" || out.Engine != aigentic.KindOllama {
		t.Errorf("result=%+v", out)
	}
}

func TestRunCSRFAndKindMappings(t *testing.T) {
	username, group := currentUser(t)
	ol := ollamaStub(t)
	defer ol.Close()
	s := newServer(t, group, ol.URL)
	access := mintAccess(t, username)
	const csrf = "csrf-token"

	// Missing CSRF on a mutating request => 403.
	if rec := do(t, s, "POST", base+"run", runBody(t, aigentic.KindOllama, aigentic.Request{Prompt: "x"}), access, ""); rec.Code != http.StatusForbidden {
		t.Errorf("missing csrf: got %d want 403", rec.Code)
	}
	// Unknown kind => 404.
	if rec := do(t, s, "POST", base+"run", runBody(t, "nope", aigentic.Request{Prompt: "x"}), access, csrf); rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind: got %d want 404", rec.Code)
	}
	// Paid kind passes the api-right gate for an admin, then 503 (no API key configured).
	if rec := do(t, s, "POST", base+"run", runBody(t, aigentic.KindClaudeAPI, aigentic.Request{Prompt: "x"}), access, csrf); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unavailable engine: got %d want 503 (%s)", rec.Code, rec.Body)
	}
}

func TestRunDeniedWithoutRunRight(t *testing.T) {
	username, _ := currentUser(t)
	// adminGroup set to a group the user is NOT in => not admin, and lacks hp_aigentic_run.
	s := newServer(t, "hp_aigentic_nonexistent_admin", "")
	access := mintAccess(t, username)
	if rec := do(t, s, "POST", base+"run", runBody(t, aigentic.KindOllama, aigentic.Request{Prompt: "x"}), access, "csrf"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-member: got %d want 403", rec.Code)
	}
}

const testKey = "sk-ant-test-0123456789abcdef" // valid shape for the key store

// An admin can read status, set, replace and clear the key — and the key value never appears
// in any response body (only a masked hint).
func TestSecretAdminFlow(t *testing.T) {
	username, group := currentUser(t)
	s := newServer(t, group, "") // admin = primary group
	access := mintAccess(t, username)
	const csrf = "csrf-token"

	// Initially unconfigured.
	rec := do(t, s, "GET", base+"secret", nil, access, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET secret: %d %s", rec.Code, rec.Body)
	}
	var st struct {
		Configured bool   `json:"configured"`
		Source     string `json:"source"`
		Hint       string `json:"hint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Configured {
		t.Fatalf("fresh store reports configured: %+v", st)
	}

	// Set a key.
	rec = do(t, s, "POST", base+"secret", []byte(`{"key":"`+testKey+`"}`), access, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST secret: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), testKey) {
		t.Fatal("response leaked the raw key")
	}

	// Status now reflects it (masked) without exposing the value.
	rec = do(t, s, "GET", base+"secret", nil, access, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Configured || st.Source != "store" || st.Hint == "" || strings.Contains(rec.Body.String(), testKey) {
		t.Fatalf("post-set status leaks or wrong: %s", rec.Body)
	}

	// Clear it.
	rec = do(t, s, "POST", base+"secret", []byte(`{"clear":true}`), access, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Configured {
		t.Fatalf("still configured after clear: %+v", st)
	}
}

func TestSecretRequiresAdmin(t *testing.T) {
	username, _ := currentUser(t)
	s := newServer(t, "hp_aigentic_nonexistent_admin", "") // not admin
	access := mintAccess(t, username)
	if rec := do(t, s, "GET", base+"secret", nil, access, ""); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin GET secret: got %d want 403", rec.Code)
	}
	if rec := do(t, s, "POST", base+"secret", []byte(`{"key":"`+testKey+`"}`), access, "csrf-token"); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin POST secret: got %d want 403", rec.Code)
	}
}

func TestSecretRejectsBadKeyAndMissingCSRF(t *testing.T) {
	username, group := currentUser(t)
	s := newServer(t, group, "")
	access := mintAccess(t, username)

	// A malformed key => 400.
	if rec := do(t, s, "POST", base+"secret", []byte(`{"key":"nope"}`), access, "csrf-token"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad key: got %d want 400 (%s)", rec.Code, rec.Body)
	}
	// Missing CSRF on the mutating write => 403, even for an admin.
	if rec := do(t, s, "POST", base+"secret", []byte(`{"key":"`+testKey+`"}`), access, ""); rec.Code != http.StatusForbidden {
		t.Errorf("missing csrf: got %d want 403", rec.Code)
	}
}
