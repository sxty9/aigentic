package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sxty9/aigentic/backend/internal/auth"
	"github.com/sxty9/aigentic/backend/internal/grave"
	"github.com/sxty9/prizm/graveyard"
)

// The graveyard endpoints expose the substrate as an owned, path-addressed data store — the seam
// Mercury's axiom tree is built on. They are deliberately thin: identity, capability and the
// no-silent-overwrite invariant are enforced here (the store owns its own integrity); higher-level
// policy (classification, duplicate merge, front-matter ids) lives in the calling service.
//
// content crosses the wire base64-encoded: the graveyard is a byte store, and base64 keeps binary
// and control characters unambiguous through JSON.

const maxGraveBody = 4 << 20 // 4 MiB — an axiom is small; this is generous headroom

// structured returns the substrate's structured surface, or false when the active backend does not
// implement it (memory, append-only). It is what makes the store usable for described, movable
// records rather than a bare provenance sink.
func (s *Server) structured() (grave.Structured, bool) {
	st, ok := s.grave.(grave.Structured)
	return st, ok
}

// graveList enumerates the file paths under a prefix (sorted). Requires a Listable backend.
func (s *Server) graveList(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	lister, ok := s.grave.(graveyard.Listable)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "The active graveyard backend cannot enumerate")
		return
	}
	refs, err := lister.List(r.Context(), graveyard.Ref(r.URL.Query().Get("prefix")))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "List failed")
		return
	}
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = string(ref)
	}
	writeJSON(w, http.StatusOK, map[string]any{"refs": out})
}

// graveGet reads the record at a path. A missing record is 404 (a normal outcome), so a caller can
// probe for existence before a write.
func (s *Server) graveGet(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	data, found, err := s.grave.Get(r.Context(), graveyard.Ref(path))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Get failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "No record at that path")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    path,
		"content": base64.StdEncoding.EncodeToString(data),
	})
}

// gravePut stores a record at a path with a mandatory description (scheme rejects an empty one).
// It refuses to clobber: a put onto an existing path without overwrite:true is 409, so a
// mis-addressed write can never silently destroy another record (scheme's PutStructured overwrites
// in place). The caller decides what a conflict means (merge, rename) and retries with a new path.
func (s *Server) gravePut(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	st, ok := s.structured()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "The active graveyard backend is not a structured store")
		return
	}
	var body struct {
		Path        string `json:"path"`
		Description string `json:"description"`
		Content     string `json:"content"` // base64
		Overwrite   bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGraveBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if strings.TrimSpace(body.Description) == "" {
		writeErr(w, http.StatusBadRequest, "description is required")
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "content must be base64")
		return
	}
	// The no-clobber check and the write together form ONE atomic access: without
	// serialization two concurrent puts to the same new path both pass the existence check and
	// the second silently clobbers the first — precisely the invariant this guard promises. The
	// daemon is the graveyard's sole structured writer, so one mutex over the guarded mutations
	// makes the compound access indivisible, with no observable intermediate state (Atomare
	// Zugriffe). Provenance puts use a disjoint ref namespace and never target a structured path.
	s.graveMu.Lock()
	defer s.graveMu.Unlock()
	if !body.Overwrite {
		if _, found, gerr := s.grave.Get(r.Context(), graveyard.Ref(body.Path)); gerr == nil && found {
			writeErr(w, http.StatusConflict, "A record already exists at that path")
			return
		}
	}
	ref, err := st.PutStructured(r.Context(), body.Path, body.Description, data)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Put failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": string(ref)})
}

// graveMove re-keys a record (scheme carries the content with the path). Refuses to clobber the
// destination, same as put.
func (s *Server) graveMove(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	st, ok := s.structured()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "The active graveyard backend is not a structured store")
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.From == "" || body.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	// Same atomic guard as put: the destination check and the move are one indivisible access
	// under the shared grave-write lock, so concurrent moves cannot both pass and clobber.
	s.graveMu.Lock()
	defer s.graveMu.Unlock()
	if _, found, gerr := s.grave.Get(r.Context(), graveyard.Ref(body.To)); gerr == nil && found {
		writeErr(w, http.StatusConflict, "A record already exists at the destination")
		return
	}
	if err := st.Move(r.Context(), graveyard.Ref(body.From), graveyard.Ref(body.To)); err != nil {
		writeErr(w, http.StatusBadGateway, "Move failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// graveDelete removes a record (recursively for a folder). Idempotent: an absent path is not an
// error. Requires a Deletable backend (append-only substrates do not implement it).
func (s *Server) graveDelete(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	deleter, ok := s.grave.(graveyard.Deletable)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "The active graveyard backend is append-only")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	// Take the shared grave-write lock so a delete is serialized with put/move: every structured
	// mutation is ordered, and none observes another's intermediate state (Atomare Zugriffe).
	s.graveMu.Lock()
	defer s.graveMu.Unlock()
	if err := deleter.Delete(r.Context(), graveyard.Ref(path)); err != nil {
		writeErr(w, http.StatusBadGateway, "Delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
