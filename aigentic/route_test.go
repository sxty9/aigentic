package aigentic

import (
	"context"
	"errors"
	"testing"

	"github.com/sxty9/prizm/prizm"
)

// fakeRouter records the request it was handed and returns a canned response.
type fakeRouter struct {
	resp prizm.Response
	err  error
	got  prizm.Request
}

func (f *fakeRouter) Route(_ context.Context, req prizm.Request) (prizm.Response, error) {
	f.got = req
	return f.resp, f.err
}

func TestRouteValidatesPrompt(t *testing.T) {
	fr := &fakeRouter{}
	if _, err := Route(context.Background(), fr, KindOllama, "alice", Request{Prompt: ""}); !errors.Is(err, prizm.ErrInvalidRequest) {
		t.Fatalf("empty prompt err = %v, want ErrInvalidRequest", err)
	}
}

func TestRouteStampsHeaderAndRoundTrips(t *testing.T) {
	data, err := prizm.EncodeData(Result{Output: "hi", Engine: KindOllama})
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRouter{resp: prizm.Response{Data: data}}

	res, err := Route(context.Background(), fr, KindOllama, "alice", Request{Prompt: "q"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Output != "hi" || res.Engine != KindOllama {
		t.Errorf("result = %+v", res)
	}
	// Subject is server-authoritative and Kind is set from the argument, never from the wire.
	if fr.got.Header.Subject != "alice" || fr.got.Header.Kind != KindOllama {
		t.Errorf("routed header = %+v, want subject=alice kind=ollama", fr.got.Header)
	}
}
