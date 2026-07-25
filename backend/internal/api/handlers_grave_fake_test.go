package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sxty9/aigentic/backend/internal/auth"
	secretstore "github.com/sxty9/aigentic/backend/internal/secret"
	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// fakeGrave is an in-memory graveyard that ALSO implements grave.Structured, so the /grave
// put/move handlers get real, hermetic coverage in the DEFAULT build. The stock memory backend is
// not structured (those handlers would 503), and the only other coverage sits behind the cgo
// `scheme` build tag, which needs a Rust static library absent from this host — so without this
// fake the guarded-write path is exercised by nothing the standard `go test ./...` actually runs.
//
// getErr forces the existence-check read to fail (to prove the no-clobber guard fails CLOSED);
// putDelay delays the WRITE's landing — widening the existence-check→write window so that, absent
// the serializing lock, several writers slip between another's check and its landing (a real
// TOCTOU). It must sit on the write, not the read: a delay before the read preserves the goroutines'
// entry stagger and the read→write gap stays ~0, so the race never manifests.
type fakeGrave struct {
	mu             sync.Mutex
	data           map[string][]byte
	getErr         error         // when set, Get returns this error (simulates a substrate read fault)
	putDelay       time.Duration // when >0, PutStructured sleeps before recording, widening the TOCTOU window
	structuredPuts int32         // successful PutStructured landings (atomic)
}

func newFakeGrave() *fakeGrave { return &fakeGrave{data: map[string][]byte{}} }

func (f *fakeGrave) Put(_ context.Context, ref graveyard.Ref, data []byte) (graveyard.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[string(ref)] = append([]byte(nil), data...)
	return ref, nil
}

func (f *fakeGrave) Get(_ context.Context, ref graveyard.Ref) ([]byte, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.data[string(ref)]
	return b, ok, nil
}

func (f *fakeGrave) PutStructured(_ context.Context, path, _ string, data []byte) (graveyard.Ref, error) {
	if f.putDelay > 0 {
		time.Sleep(f.putDelay) // outside the lock: model a slow landing so an unguarded check races it
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[path] = append([]byte(nil), data...)
	atomic.AddInt32(&f.structuredPuts, 1)
	return graveyard.Ref(path), nil
}

func (f *fakeGrave) Move(_ context.Context, from, to graveyard.Ref) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.data[string(from)]
	if !ok {
		return errors.New("fakeGrave: move source missing")
	}
	f.data[string(to)] = b
	delete(f.data, string(from))
	return nil
}

func (f *fakeGrave) SetDescription(_ context.Context, _, _ string) error { return nil }

// has reports whether a path is present. Safe only when no request goroutines are in flight.
func (f *fakeGrave) has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[path]
	return ok
}

// newGraveServer wires a Server around a caller-supplied graveyard, with the running account as
// admin (so it holds every right, as in the other api tests). Returns the server and the username.
func newGraveServer(t *testing.T, g graveyard.Graveyard) (*Server, string) {
	t.Helper()
	username, group := currentUser(t)
	reg := prizm.NewRegistry(0)
	td := t.TempDir()
	store := secretstore.New(td+"/anthropic.key", td+"/users", "")
	return New(auth.NewVerifier(secret, group), reg, g, store, nil, nil, ""), username
}

func gravePutBody(path, desc, content string, overwrite bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"path": path, "description": desc,
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "overwrite": overwrite,
	})
	return b
}

// TestGravePutFakeBackend covers the put handler in the default build: a fresh write lands (200),
// a no-clobber write is refused (409) WITHOUT a second landing, and an explicit overwrite writes.
func TestGravePutFakeBackend(t *testing.T) {
	f := newFakeGrave()
	s, username := newGraveServer(t, f)
	access := mintAccess(t, username)
	const csrf = "csrf-token"

	if rec := do(t, s, "POST", base+"grave/put", gravePutBody("axiome/a.md", "desc", "one", false), access, csrf); rec.Code != http.StatusOK {
		t.Fatalf("put fresh: got %d want 200 (%s)", rec.Code, rec.Body)
	}
	if rec := do(t, s, "POST", base+"grave/put", gravePutBody("axiome/a.md", "desc2", "two", false), access, csrf); rec.Code != http.StatusConflict {
		t.Fatalf("no-clobber: got %d want 409 (%s)", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&f.structuredPuts); got != 1 {
		t.Fatalf("a 409 must not write: structured landings = %d, want 1", got)
	}
	if rec := do(t, s, "POST", base+"grave/put", gravePutBody("axiome/a.md", "desc3", "three", true), access, csrf); rec.Code != http.StatusOK {
		t.Fatalf("overwrite:true: got %d want 200 (%s)", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&f.structuredPuts); got != 2 {
		t.Fatalf("overwrite must write: structured landings = %d, want 2", got)
	}
}

// TestGravePutFailsClosedOnCheckError proves the no-clobber guard fails CLOSED: when the existence
// read faults, the put must be refused (502) and NOTHING may be written — never fall through to a
// blind write that could clobber a record we simply could not read (Daten-Integrität).
func TestGravePutFailsClosedOnCheckError(t *testing.T) {
	f := newFakeGrave()
	f.getErr = errors.New("substrate read fault")
	s, username := newGraveServer(t, f)
	access := mintAccess(t, username)

	rec := do(t, s, "POST", base+"grave/put", gravePutBody("axiome/x.md", "d", "v", false), access, "csrf-token")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("errored existence check must fail closed: got %d want 502 (%s)", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&f.structuredPuts); got != 0 {
		t.Fatalf("fail-closed put must not write: structured landings = %d, want 0", got)
	}
}

// TestGraveMoveFailsClosedOnCheckError is the move counterpart: an unverifiable destination refuses
// (502), leaving the source in place and the destination uncreated.
func TestGraveMoveFailsClosedOnCheckError(t *testing.T) {
	f := newFakeGrave()
	if _, err := f.Put(context.Background(), "from.md", []byte("v")); err != nil {
		t.Fatal(err)
	}
	f.getErr = errors.New("substrate read fault")
	s, username := newGraveServer(t, f)
	access := mintAccess(t, username)

	mv, _ := json.Marshal(map[string]string{"from": "from.md", "to": "to.md"})
	rec := do(t, s, "POST", base+"grave/move", mv, access, "csrf-token")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("errored destination check must fail closed: got %d want 502 (%s)", rec.Code, rec.Body)
	}
	if !f.has("from.md") {
		t.Fatal("fail-closed move removed the source")
	}
	if f.has("to.md") {
		t.Fatal("fail-closed move created the destination")
	}
}

// TestGravePutFakeConcurrentSingleWinner hammers one fresh path with concurrent put-if-absent
// writes. The check-then-write is a compound access; only the shared grave-write lock makes it
// indivisible. Exactly one writer may win (200) and exactly one record may land — every other must
// see 409 (Atomare Zugriffe: unteilbar, ohne beobachtbaren Zwischenzustand). putDelay widens the
// check→write window so a regression that drops the lock would let several writers through and fail
// this test (verified: with the lock removed, this test does report multiple winners).
func TestGravePutFakeConcurrentSingleWinner(t *testing.T) {
	f := newFakeGrave()
	f.putDelay = 40 * time.Millisecond
	s, username := newGraveServer(t, f)
	access := mintAccess(t, username)
	const csrf = "csrf-token"
	const path = "axiome/race.md"

	const n = 10
	codes := make(chan int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := gravePutBody(path, "contested", fmt.Sprintf("writer-%d", i), false)
			<-start // release all writers at once to maximise contention
			codes <- do(t, s, "POST", base+"grave/put", body, access, csrf).Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(codes)

	winners, conflicts := 0, 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if winners != 1 {
		t.Fatalf("put-if-absent not atomic: %d winners (want 1), %d conflicts", winners, conflicts)
	}
	if got := atomic.LoadInt32(&f.structuredPuts); got != 1 {
		t.Fatalf("exactly one record must land: structured landings = %d, want 1", got)
	}
}
