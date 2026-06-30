// Package api serves the aigentic service's HTTP surface under /api/services/aigentic/,
// behind the shared holistic session. It is intentionally thin: it authenticates, gates
// and then hands the request to the prizm Registry, which routes on Header.Kind to a
// registered processor. The base never decodes Data (the OSI-switch property). Error
// bodies match holistic's contract: {"detail": "..."}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/auth"
	"github.com/sxty9/aigentic/backend/internal/rights"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/prizm/prizm"
)

const (
	base       = "/api/services/aigentic/"
	service    = "aigentic"
	version    = "0.1.0"
	maxBody    = 1 << 20  // 1 MiB request cap (credential endpoints)
	maxRunBody = 32 << 20 // 32 MiB for /run — multimodal inline (base64 images/PDFs); Anthropic caps at 32 MB
)

// Server wires the session verifier, the processor registry and the admin-managed API-key
// store into HTTP handlers.
type Server struct {
	v            *auth.Verifier
	reg          *prizm.Registry
	sec          *secretstore.Store                      // admin-managed Anthropic key; nil disables the secret endpoints
	ollamaModels func(context.Context) ([]string, error) // lists local ollama models for the picker; nil => none
}

// New builds a server. sec may be nil (the /secret endpoints then report 503); ollamaModels may
// be nil (the /models endpoint then returns no local models).
func New(v *auth.Verifier, reg *prizm.Registry, sec *secretstore.Store, ollamaModels func(context.Context) ([]string, error)) *Server {
	return &Server{v: v, reg: reg, sec: sec, ollamaModels: ollamaModels}
}

type handler func(w http.ResponseWriter, r *http.Request, u *auth.User)

// Handler returns the routed http.Handler (Go 1.22 method+path patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Public to any signed-in holistic user: service identity + registered kinds.
	mux.HandleFunc("GET "+base+"info", s.guard("", false, s.info))
	// Rights-gated write: run a processor. CSRF double-submit guard required. A second,
	// Kind-aware right (hp_aigentic_api) is enforced inside run() for the paid engines.
	mux.HandleFunc("POST "+base+"run", s.guard(rights.GroupRun, true, s.run))
	// Admin-only: manage the GLOBAL (shared fallback) Anthropic key. Gated on admin; CSRF on
	// writes; the key value is never returned.
	mux.HandleFunc("GET "+base+"secret", s.guardAdmin(false, s.secretStatus))
	mux.HandleFunc("POST "+base+"secret", s.guardAdmin(true, s.secretSet))
	// Per-user self-service (gated on the run right, not admin): each user links their OWN
	// Anthropic API key and Claude subscription token. CSRF on writes; secrets never returned.
	mux.HandleFunc("GET "+base+"mykey", s.guard(rights.GroupRun, false, s.myKeyStatus))
	mux.HandleFunc("POST "+base+"mykey", s.guard(rights.GroupRun, true, s.myKeySet)) // {key} | {clear:true}
	mux.HandleFunc("GET "+base+"claude", s.guard(rights.GroupRun, false, s.claudeStatus))
	mux.HandleFunc("POST "+base+"claude/link", s.guard(rights.GroupRun, true, s.claudeLink)) // {token}
	mux.HandleFunc("POST "+base+"claude/unlink", s.guard(rights.GroupRun, true, s.claudeUnlink))
	// Available models per engine (for the Files "Ask AI" picker): static Claude list + the
	// locally-pulled ollama models. Names aren't sensitive; gate on the run right.
	mux.HandleFunc("GET "+base+"models", s.guard(rights.GroupRun, false, s.modelsList))
	mux.HandleFunc("GET "+base+"health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

// guardAdmin authenticates and requires admin (operator-only actions), optionally enforcing
// CSRF. Distinct from guard's fine-grained right: configuring a money-spending secret is a
// deployment action, so admin alone gates it (admins implicitly hold every right anyway).
func (s *Server) guardAdmin(csrf bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.v.User(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "This action is restricted to administrators")
			return
		}
		if csrf && !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u)
	}
}

// guard authenticates, optionally requires a fine-grained right (perm != "" ⇒ admin or
// membership in the backing group), and optionally enforces CSRF.
func (s *Server) guard(perm string, csrf bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.v.User(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		if perm != "" && !u.Can(perm) {
			writeErr(w, http.StatusForbidden, "You do not have permission for this action")
			return
		}
		if csrf && !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u)
	}
}

// info echoes the resolved identity and the kinds the registry can route.
func (s *Server) info(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": service,
		"version": version,
		"user":    u.Username,
		"isAdmin": u.IsAdmin,
		"kinds":   s.reg.Kinds(),
	})
}

// run decodes ONLY Header₀ + opaque Data, stamps the server-authoritative subject, gates
// the paid engines on hp_aigentic_api (reading Header.Kind, the routing field — never
// Data), and routes. It never inspects Data.
func (s *Server) run(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var req prizm.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	// Subject is server-authoritative: derive it from the holistic identity, never trust
	// whatever the wire claimed.
	req.Header.Subject = u.Username

	// The paid Claude API (and the choose router, which may select it) needs the cost right.
	if paidKind(req.Header.Kind) && !u.Can(rights.GroupAPI) {
		writeErr(w, http.StatusForbidden, "The paid Claude API requires the aigentic 'cost:api' right")
		return
	}

	resp, err := s.reg.Route(r.Context(), req)
	switch {
	case errors.Is(err, prizm.ErrNoSuchKind):
		writeErr(w, http.StatusNotFound, "Unknown aigentic kind")
	case errors.Is(err, prizm.ErrDepthExceeded):
		writeErr(w, http.StatusUnprocessableEntity, "Sub-prizm recursion limit exceeded")
	case errors.Is(err, prizm.ErrInvalidRequest):
		writeErr(w, http.StatusBadRequest, "Invalid request")
	case errors.Is(err, aigentic.ErrProcessorUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "The selected engine is unavailable")
	case errors.Is(err, prizm.ErrNoSpawner):
		writeErr(w, http.StatusInternalServerError, "Router misconfigured")
	case err != nil:
		writeErr(w, http.StatusBadGateway, "Processor failed")
	default:
		writeJSON(w, http.StatusOK, resp)
	}
}

// secretStatus reports the GLOBAL (shared fallback) key's status — NEVER the key itself.
func (s *Server) secretStatus(w http.ResponseWriter, _ *http.Request, _ *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.GlobalStatus())
}

// secretSet stores/clears the GLOBAL (shared fallback) key. Admin-only + CSRF.
func (s *Server) secretSet(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	var body struct {
		Key   string `json:"key"`
		Clear bool   `json:"clear"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.Clear {
		if err := s.sec.ClearGlobal(); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not clear the key")
			return
		}
		writeJSON(w, http.StatusOK, s.sec.GlobalStatus())
		return
	}
	if err := s.sec.SetGlobal(body.Key); err != nil {
		if errors.Is(err, secretstore.ErrInvalidKey) {
			writeErr(w, http.StatusBadRequest, "That does not look like an Anthropic API key (sk-ant-…)")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not store the key")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.GlobalStatus())
}

// --- per-user self-service (subject = the server-stamped username) ---

// myKeyStatus reports the requesting user's EFFECTIVE api-key status (own → shared → env).
func (s *Server) myKeyStatus(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.UserKeyStatus(u.Username))
}

// myKeySet stores the requesting user's OWN Anthropic API key, or clears it ({"clear":true}).
func (s *Server) myKeySet(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	var body struct {
		Key   string `json:"key"`
		Clear bool   `json:"clear"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.Clear {
		if err := s.sec.ClearUserKey(u.Username); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not clear your key")
			return
		}
		writeJSON(w, http.StatusOK, s.sec.UserKeyStatus(u.Username))
		return
	}
	if err := s.sec.SetUserKey(u.Username, body.Key); err != nil {
		if errors.Is(err, secretstore.ErrInvalidKey) {
			writeErr(w, http.StatusBadRequest, "That does not look like an Anthropic API key (sk-ant-…)")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not store your key")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.UserKeyStatus(u.Username))
}

// claudeStatus reports whether the requesting user has linked a Claude subscription (masked).
func (s *Server) claudeStatus(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.TokenStatus(u.Username))
}

// claudeLink stores the requesting user's `claude setup-token` (subscription). CSRF-gated; the
// token never crosses back.
func (s *Server) claudeLink(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := s.sec.LinkToken(u.Username, body.Token); err != nil {
		if errors.Is(err, secretstore.ErrInvalidToken) {
			writeErr(w, http.StatusBadRequest, "That does not look like a Claude setup-token (run `claude setup-token`)")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not store your token")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.TokenStatus(u.Username))
}

// claudeUnlink removes the requesting user's Claude subscription token + CLI session dir.
func (s *Server) claudeUnlink(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	if err := s.sec.UnlinkToken(u.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not unlink your Claude")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.TokenStatus(u.Username))
}

// modelsList reports the models the "Ask AI" picker can offer per engine: a static Claude list
// (used by claude-cli + claude-api) and the locally-pulled ollama models (used by the local
// engine). Best-effort: ollama unreachable => an empty local list.
func (s *Server) modelsList(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	out := map[string]any{
		"claude": []map[string]string{
			{"id": "claude-sonnet-4-6", "label": "Sonnet"},
			{"id": "claude-opus-4-8", "label": "Opus"},
			{"id": "claude-haiku-4-5", "label": "Haiku"},
		},
		"ollama": []string{},
	}
	if s.ollamaModels != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if m, err := s.ollamaModels(ctx); err == nil && len(m) > 0 {
			out["ollama"] = m
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// paidKind reports whether a kind can reach the metered Anthropic API.
func paidKind(k prizm.Kind) bool {
	return k == aigentic.KindClaudeAPI || k == aigentic.KindChoose
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
