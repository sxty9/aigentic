//go:build lakearch

package lakegrave

import (
	"context"
	"testing"

	"github.com/sxty9/prizm/graveyard"
)

func TestPutGetRoundTrip(t *testing.T) {
	k, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer k.Close()
	ctx := context.Background()

	ref, err := k.Put(ctx, "", []byte("hello lakearch"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(ref) != 2*contentIDLen {
		t.Fatalf("ref len=%d want %d (%q)", len(ref), 2*contentIDLen, ref)
	}

	got, found, err := k.Get(ctx, ref)
	if err != nil || !found || string(got) != "hello lakearch" {
		t.Fatalf("get => %q found=%v err=%v", got, found, err)
	}

	// Content-addressed: identical bytes dedup to the same ref.
	if ref2, _ := k.Put(ctx, "", []byte("hello lakearch")); ref2 != ref {
		t.Errorf("dedup: %q != %q", ref2, ref)
	}

	// An absent id is found=false, not an error (VANISH).
	bad := graveyard.Ref("00" + string(ref)[2:])
	if _, found, err := k.Get(ctx, bad); err != nil || found {
		t.Errorf("absent get: found=%v err=%v (want false,nil)", found, err)
	}
}

func TestGetLargeValueBufferRetry(t *testing.T) {
	k, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer k.Close()
	ctx := context.Background()

	// Larger than the 4 KiB probe buffer => exercises the BUFFER_TOO_SMALL retry path.
	big := make([]byte, 20000)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	ref, err := k.Put(ctx, "", big)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := k.Get(ctx, ref)
	if err != nil || !found || len(got) != len(big) || string(got) != string(big) {
		t.Fatalf("large get: len=%d found=%v err=%v", len(got), found, err)
	}
}
