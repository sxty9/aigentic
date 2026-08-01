package aigentic

import (
	"context"
	"testing"

	"github.com/sxty9/prizm/graveyard"
	"github.com/sxty9/prizm/prizm"
)

// leafReturning is a stub leaf that emits a fixed Result — enough to exercise the metering
// decorator without a live engine.
func leafReturning(res Result) prizm.Processor {
	return prizm.NewTyped(func(_ context.Context, _ Request, _ prizm.Env) (Result, error) {
		return res, nil
	})
}

// runVia routes a request for kind through a one-leaf registry and returns any error.
func runProc(t *testing.T, proc prizm.Processor) {
	t.Helper()
	req := prizm.Request{Header: prizm.Header{Kind: KindOllama}}
	if _, err := proc.Process(context.Background(), req, prizm.Env{}); err != nil {
		t.Fatalf("process: %v", err)
	}
}

func TestMeterProcessorReportsUsage(t *testing.T) {
	var gotKind prizm.Kind
	var gotUsage Usage
	calls := 0
	m := meterProcessor{
		inner: leafReturning(Result{Engine: KindOllama, Usage: Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}}),
		kind:  KindOllama,
		sink:  func(k prizm.Kind, u Usage) { calls++; gotKind, gotUsage = k, u },
	}
	runProc(t, m)

	if calls != 1 {
		t.Fatalf("sink called %d times, want 1", calls)
	}
	if gotKind != KindOllama {
		t.Errorf("kind = %q, want %q", gotKind, KindOllama)
	}
	if gotUsage.InputTokens != 10 || gotUsage.OutputTokens != 20 {
		t.Errorf("usage = %+v, want in=10 out=20", gotUsage)
	}
}

func TestMeterProcessorSkipsZeroUsage(t *testing.T) {
	calls := 0
	m := meterProcessor{
		inner: leafReturning(Result{Engine: KindOllama}), // no tokens
		kind:  KindOllama,
		sink:  func(prizm.Kind, Usage) { calls++ },
	}
	runProc(t, m)
	if calls != 0 {
		t.Errorf("sink called %d times for a zero-usage result, want 0", calls)
	}
}

func TestRegisterWiresMeterOnlyWhenSet(t *testing.T) {
	// With OnUsage set, Register must succeed and wrap the leaves (behaviour verified by the
	// decorator tests above); this guards the wiring compiles and registers all five kinds
	// (the three leaves + the choose and extract routers).
	reg := prizm.NewRegistry(4)
	cfg := Config{OnUsage: func(prizm.Kind, Usage) {}}
	if err := Register(reg, graveyard.NewMemory(), cfg); err != nil {
		t.Fatalf("Register with OnUsage: %v", err)
	}
	if got := len(reg.Kinds()); got != 5 {
		t.Errorf("registered %d kinds, want 5", got)
	}
}
