//go:build lakearch

// Package lakegrave implements graveyard.Graveyard on top of lakearch — the Rust,
// content-addressed (BLAKE3), append-only substrate — via its C-ABI FFI. Put maps to
// lakearch_append (returning the content id), Get to lakearch_get_by_content_id (a read
// through the access gate, with the buffer-too-small retry protocol). It is append-only,
// so it implements neither Deletable nor Listable.
//
// Built only with `-tags lakearch`: it needs cgo and links liblakearch_ffi. The FFI is an
// in-process embedding within ONE trust zone — this process is the trusted layer above it
// (see lakearch_ffi.h). The graveyard's Subject-scoped path confinement lives a layer up,
// in the aigentic context assembly.
package lakegrave

/*
#cgo CFLAGS: -I${SRCDIR}/../../../lakearch/crates/lakearch-ffi/include
#cgo LDFLAGS: ${SRCDIR}/../../../lakearch/target/release/liblakearch_ffi.a -lpthread -ldl -lm
#include <stdlib.h>
#include "lakearch_ffi.h"
*/
import "C"

import (
	"context"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/sxty9/prizm/graveyard"
)

// contentIDLen must equal the C macro LAKEARCH_CONTENT_ID_LEN (part of the ABI contract).
const contentIDLen = 32

// Kernel is an open lakearch kernel exposed as a graveyard.Graveyard.
type Kernel struct {
	mu     sync.Mutex
	handle *C.KernelHandle
}

var _ graveyard.Graveyard = (*Kernel)(nil)

// Open opens (creating if needed) a lakearch kernel rooted at dir.
func Open(dir string) (*Kernel, error) {
	cdir := C.CString(dir)
	defer C.free(unsafe.Pointer(cdir))
	var h *C.KernelHandle
	st := C.lakearch_open(cdir, C.size_t(len(dir)), &h)
	if st != C.LAKEARCH_OK {
		return nil, statusErr("open", st)
	}
	k := &Kernel{handle: h}
	// Safety net if a caller forgets Close(); explicit Close() clears this finalizer.
	runtime.SetFinalizer(k, func(k *Kernel) { _ = k.Close() })
	return k, nil
}

// Close releases the kernel handle. Idempotent here; the FFI forbids a double close, which
// the nil guard prevents.
func (k *Kernel) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.handle == nil {
		return nil
	}
	st := C.lakearch_close(k.handle)
	k.handle = nil
	runtime.SetFinalizer(k, nil)
	if st != C.LAKEARCH_OK {
		return statusErr("close", st)
	}
	return nil
}

// Put appends data and returns its BLAKE3 content id as a hex Ref. The supplied ref is
// ignored: lakearch is content-addressed, which the graveyard contract explicitly permits
// ("an append-only or content-addressed backend MAY ignore the supplied ref"). Identical
// bytes dedup to the same id.
func (k *Kernel) Put(_ context.Context, _ graveyard.Ref, data []byte) (graveyard.Ref, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.handle == nil {
		return "", fmt.Errorf("lakegrave: kernel closed")
	}
	var id [contentIDLen]C.uint8_t
	var dptr *C.uint8_t
	if len(data) > 0 {
		dptr = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}
	st := C.lakearch_append(k.handle, dptr, C.size_t(len(data)), &id[0])
	if st != C.LAKEARCH_OK {
		return "", statusErr("append", st)
	}
	raw := C.GoBytes(unsafe.Pointer(&id[0]), contentIDLen)
	return graveyard.Ref(hex.EncodeToString(raw)), nil
}

// Get reads the record at ref (a hex content id) through the gate. A hidden or absent
// record is found=false with no error (VANISH; the graveyard contract treats absence as a
// normal outcome).
func (k *Kernel) Get(_ context.Context, ref graveyard.Ref) ([]byte, bool, error) {
	id, err := hex.DecodeString(string(ref))
	if err != nil || len(id) != contentIDLen {
		return nil, false, fmt.Errorf("lakegrave: bad ref %q", ref)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.handle == nil {
		return nil, false, fmt.Errorf("lakegrave: kernel closed")
	}
	cid := (*C.uint8_t)(unsafe.Pointer(&id[0]))

	// Buffer protocol: probe with a starter buffer; on BUFFER_TOO_SMALL, out_len carries
	// the needed size, so retry once with exactly that.
	buf := C.malloc(4096)
	defer func() { C.free(buf) }()
	outLen := C.size_t(4096)
	st := C.lakearch_get_by_content_id(k.handle, cid, (*C.uint8_t)(buf), &outLen)
	if st == C.LAKEARCH_BUFFER_TOO_SMALL {
		C.free(buf)
		buf = C.malloc(outLen)
		st = C.lakearch_get_by_content_id(k.handle, cid, (*C.uint8_t)(buf), &outLen)
	}
	switch st {
	case C.LAKEARCH_OK:
		// lakearch returns the Datum's canonical CBOR (§K4), not the raw payload. Everything
		// this adapter stores is a leaf, so unwrap the leaf back to the bytes Put received —
		// a faithful blob round-trip (the graveyard contract's "read it back").
		payload, err := unwrapLeaf(C.GoBytes(buf, C.int(outLen)))
		if err != nil {
			return nil, false, err
		}
		return payload, true, nil
	case C.LAKEARCH_NOT_FOUND:
		return nil, false, nil
	default:
		return nil, false, statusErr("get", st)
	}
}

var errTruncatedDatum = fmt.Errorf("lakegrave: truncated datum cbor")

// unwrapLeaf extracts the payload from the canonical CBOR of a lakearch leaf Datum.
// lakearch encodes Datum::leaf(payload) as the 1-entry map {0: bstr(payload)}:
//
//	0xA1 (map, 1 pair)  0x00 (key: unsigned int 0)  <CBOR byte string: the payload>
//
// The byte string's length uses CBOR's standard additional-information encoding. Because
// append() only ever stores leaves, this unwrap is total for anything Get reads back.
func unwrapLeaf(b []byte) ([]byte, error) {
	if len(b) < 3 || b[0] != 0xA1 || b[1] != 0x00 {
		return nil, fmt.Errorf("lakegrave: unexpected datum framing %x", b[:min(3, len(b))])
	}
	ib := b[2]
	if ib&0xE0 != 0x40 { // CBOR major type 2 = byte string
		return nil, fmt.Errorf("lakegrave: datum value is not a byte string (0x%02x)", ib)
	}
	off := 3
	var n int
	switch ai := ib & 0x1F; {
	case ai <= 23:
		n = int(ai)
	case ai == 24:
		if len(b) < off+1 {
			return nil, errTruncatedDatum
		}
		n = int(b[off])
		off++
	case ai == 25:
		if len(b) < off+2 {
			return nil, errTruncatedDatum
		}
		n = int(b[off])<<8 | int(b[off+1])
		off += 2
	case ai == 26:
		if len(b) < off+4 {
			return nil, errTruncatedDatum
		}
		n = int(b[off])<<24 | int(b[off+1])<<16 | int(b[off+2])<<8 | int(b[off+3])
		off += 4
	case ai == 27:
		if len(b) < off+8 {
			return nil, errTruncatedDatum
		}
		var v uint64
		for i := 0; i < 8; i++ {
			v = v<<8 | uint64(b[off+i])
		}
		off += 8
		n = int(v)
	default:
		return nil, fmt.Errorf("lakegrave: indefinite-length byte string unsupported")
	}
	if n < 0 || len(b) < off+n {
		return nil, errTruncatedDatum
	}
	return b[off : off+n], nil
}

func statusErr(op string, st C.LakearchStatus) error {
	return fmt.Errorf("lakegrave: %s: lakearch status %d", op, int(st))
}
