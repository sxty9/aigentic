// Package api serves the aigentic service's HTTP surface under /api/services/aigentic/,
// behind the shared holistic session. It is intentionally thin: it authenticates, gates
// and then hands the request to the prizm Registry, which routes on Header.Kind to a
// registered processor. The base never decodes Data (the OSI-switch property). Error
// bodies match holistic's contract: {"detail": "..."}.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/auth"
	"github.com/sxty9/aigentic/backend/internal/rights"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/prizm/prizm"
)

const (
	base    = "/api/services/aigentic/"
	service = "aigentic"
	version = "0.1.0"
	maxBody = 1 << 20 // 1 MiB request cap
)

// Server wires the session verifier, the processor registry and the admin-managed API-key
// store into HTTP handlers.
type Server struct {
	v   *auth.Verifier
	reg *prizm.Registry
	sec *secretstore.Store // admin-managed Anthropic key; nil disables the secret endpoints
}

// New builds a server. sec may be nil (the /secret endpoints then report 503).
func New(v *auth.Verifier, reg *prizm.Registry, sec *secretstore.Store) *Server {
	return &Server{v: v, reg: reg, sec: sec}
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
	// Admin-only: manage the Anthropic API key. Reading status (masked) and writing/clearing
	// are operator actions, not a per-user capability — gated on admin, not a fine-grained
	// right. The write is CSRF-guarded; the key value is never returned.
	mux.HandleFunc("GET "+base+"secret", s.guardAdmin(false, s.secretStatus))
	mux.HandleFunc("POST "+base+"secret", s.guardAdmin(true, s.secretSet))
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
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

// secretStatus reports whether the Anthropic API key is configured, its source and a masked
// hint — NEVER the key itself.
func (s *Server) secretStatus(w http.ResponseWriter, _ *http.Request, _ *auth.User) {
	if s.sec == nil {
		writeErr(w, http.StatusServiceUnavailable, "Key store not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.Status())
}

// secretSet stores a new key, or clears it ({"clear": true}). The request body is the only
// place a key crosses the wire (inbound, over the holistic TLS session); the response echoes
// status only, never the key.
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
		if err := s.sec.Clear(); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not clear the key")
			return
		}
		writeJSON(w, http.StatusOK, s.sec.Status())
		return
	}
	if err := s.sec.Set(body.Key); err != nil {
		if errors.Is(err, secretstore.ErrInvalidKey) {
			writeErr(w, http.StatusBadRequest, "That does not look like an Anthropic API key (sk-ant-…)")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not store the key")
		return
	}
	writeJSON(w, http.StatusOK, s.sec.Status())
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
