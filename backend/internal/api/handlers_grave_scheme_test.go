//go:build scheme

package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/sxty9/aigentic/aigentic"
	"github.com/sxty9/aigentic/backend/internal/auth"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/aigentic/graveyard/schemegrave"
	"github.com/sxty9/prizm/prizm"
)

// newSchemeServer builds a Server backed by a real scheme store in a temp dir — the same substrate
// Mercury's axioms live on. The admin group is the running account's primary group, so the test
// user holds every right (as in the other api tests).
func newSchemeServer(t *testing.T) (*Server, func()) {
	t.Helper()
	g, err := schemegrave.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open scheme: %v", err)
	}
	reg := prizm.NewRegistry(0)
	if err := aigentic.Register(reg, g, aigentic.Config{}); err != nil {
		t.Fatal(err)
	}
	td := t.TempDir()
	store := secretstore.New(td+"/anthropic.key", td+"/users", "")
	_, group := currentUser(t)
	return New(auth.NewVerifier(secret, group), reg, g, store, nil, nil, ""), func() { _ = g.Close() }
}

// TestGraveEndpointsRoundTrip drives the owned-store surface end to end against a real scheme
// backend: put -> get -> list -> move -> delete, plus the no-silent-overwrite invariant that guards
// against a mis-addressed write destroying another axiom.
func TestGraveEndpointsRoundTrip(t *testing.T) {
	s, cleanup := newSchemeServer(t)
	defer cleanup()
	username, _ := currentUser(t)
	access := mintAccess(t, username)
	const csrf = "csrf-token"

	put := func(path, desc, content string, overwrite bool) *http.Response {
		body, _ := json.Marshal(map[string]any{
			"path": path, "description": desc,
			"content": base64.StdEncoding.EncodeToString([]byte(content)), "overwrite": overwrite,
		})
		return do(t, s, "POST", base+"grave/put", body, access, csrf).Result()
	}

	// put a first axiom
	if r := put("axiome/architektur/ssot.md", "Single Source of Truth", "kein paralleler Datenpfad", false); r.StatusCode != 200 {
		t.Fatalf("put: got %d", r.StatusCode)
	}

	// get it back
	rec := do(t, s, "GET", base+"grave/get?path=axiome/architektur/ssot.md", nil, access, "")
	if rec.Code != 200 {
		t.Fatalf("get: got %d", rec.Code)
	}
	var got struct{ Content string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if b, _ := base64.StdEncoding.DecodeString(got.Content); string(b) != "kein paralleler Datenpfad" {
		t.Fatalf("get content = %q", b)
	}

	// no-silent-overwrite: a second put onto the same path without overwrite must 409, NOT clobber
	if r := put("axiome/architektur/ssot.md", "Andere", "GANZ ANDERER INHALT", false); r.StatusCode != http.StatusConflict {
		t.Fatalf("overwrite guard: got %d, want 409", r.StatusCode)
	}
	// and the original must be intact
	rec = do(t, s, "GET", base+"grave/get?path=axiome/architektur/ssot.md", nil, access, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if b, _ := base64.StdEncoding.DecodeString(got.Content); string(b) != "kein paralleler Datenpfad" {
		t.Fatalf("original was clobbered: %q", b)
	}

	// a second axiom in another category, then list the tree
	if r := put("axiome/minimalismus/keine-tooltips.md", "Keine Tooltips", "intuitiv by design", false); r.StatusCode != 200 {
		t.Fatalf("put 2: got %d", r.StatusCode)
	}
	rec = do(t, s, "GET", base+"grave/list?prefix=axiome", nil, access, "")
	var list struct{ Refs []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Refs) != 2 {
		t.Fatalf("list = %v, want 2 files", list.Refs)
	}

	// move (re-file) an axiom
	mv, _ := json.Marshal(map[string]string{"from": "axiome/minimalismus/keine-tooltips.md", "to": "axiome/ui/keine-tooltips.md"})
	if rec := do(t, s, "POST", base+"grave/move", mv, access, csrf); rec.Code != 200 {
		t.Fatalf("move: got %d", rec.Code)
	}
	if rec := do(t, s, "GET", base+"grave/get?path=axiome/ui/keine-tooltips.md", nil, access, ""); rec.Code != 200 {
		t.Fatalf("moved axiom not at new path: %d", rec.Code)
	}

	// delete, then confirm it is gone
	if rec := do(t, s, "DELETE", base+"grave?path=axiome/ui/keine-tooltips.md", nil, access, csrf); rec.Code != 200 {
		t.Fatalf("delete: got %d", rec.Code)
	}
	if rec := do(t, s, "GET", base+"grave/get?path=axiome/ui/keine-tooltips.md", nil, access, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted axiom still present: %d", rec.Code)
	}

	// an empty description is rejected (scheme's mandatory Beschreibung)
	if r := put("axiome/x/leer.md", "  ", "irgendwas", false); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty description: got %d, want 400", r.StatusCode)
	}
}

// TestGravePutIfAbsentIsAtomic hammers one fresh path with concurrent put-if-absent writes. The
// no-clobber guard is a check-then-act compound access; without serialization two writers both
// pass the existence check and the second silently clobbers the first. Exactly one writer must win
// (200), every other must see 409, and the surviving record must be — byte for byte — the winner's,
// never a clobber or a partial (Atomare Zugriffe: unteilbar, ohne beobachtbaren Zwischenzustand).
func TestGravePutIfAbsentIsAtomic(t *testing.T) {
	s, cleanup := newSchemeServer(t)
	defer cleanup()
	username, _ := currentUser(t)
	access := mintAccess(t, username)
	const csrf = "csrf-token"
	const path = "axiome/race/contested.md"

	const n = 12
	type outcome struct{ idx, code int }
	out := make(chan outcome, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"path": path, "description": "contested",
				"content":   base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("writer-%d", i))),
				"overwrite": false,
			})
			<-start // release all writers at once to maximise contention
			out <- outcome{i, do(t, s, "POST", base+"grave/put", body, access, csrf).Result().StatusCode}
		}(i)
	}
	close(start)
	wg.Wait()
	close(out)

	winners, conflicts, winner := 0, 0, -1
	for o := range out {
		switch o.code {
		case http.StatusOK:
			winners, winner = winners+1, o.idx
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("writer %d: unexpected status %d", o.idx, o.code)
		}
	}
	if winners != 1 {
		t.Fatalf("put-if-absent not atomic: %d writers won (want exactly 1), %d saw 409", winners, conflicts)
	}
	// The one surviving record must be exactly the winner's bytes — never a clobber or a partial.
	rec := do(t, s, "GET", base+"grave/get?path="+path, nil, access, "")
	var got struct{ Content string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	b, _ := base64.StdEncoding.DecodeString(got.Content)
	if want := fmt.Sprintf("writer-%d", winner); string(b) != want {
		t.Fatalf("surviving record = %q, want the winner's %q (clobbered)", b, want)
	}
}
