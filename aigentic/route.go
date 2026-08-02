package aigentic

import (
	"context"

	"github.com/sxty9/prizm/prizm"
)

// Router is the subset of *prizm.Registry the Ask helper needs: dispatch a top-level request.
type Router interface {
	Route(ctx context.Context, req prizm.Request) (prizm.Response, error)
}

// Route dispatches a typed aigentic.Request through the registry under the given kind and returns
// the typed Result. It is the P-layer entry point for the non-passthrough surfaces (today the MCP
// door): they hand it a typed Request and receive a typed Result, so the HTTP shell never encodes
// or decodes Data₀ itself and keeps its OSI-switch property (it routes on Header.Kind alone).
//
// subject is server-authoritative: the caller passes the resolved holistic identity, never a value
// from the wire. Kind must be one aigentic serves; an unknown kind surfaces as prizm.ErrNoSuchKind
// from the registry.
func Route(ctx context.Context, r Router, kind prizm.Kind, subject string, in Request) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}
	data, err := prizm.EncodeData(in)
	if err != nil {
		return Result{}, err
	}
	req := prizm.Request{Header: prizm.Header{Kind: kind, Subject: subject}, Data: data}
	resp, err := r.Route(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return prizm.DecodeData[Result](resp.Data)
}
