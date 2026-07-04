//go:build scheme

// Package schemegrave implements graveyard.Graveyard — plus the optional
// graveyard.Deletable and graveyard.Listable capabilities, and scheme's own
// richer Structured surface — on top of scheme, the Rust, MUTABLE, path-addressed,
// clearly-structured filesystem-tree substrate, via its C-ABI FFI.
//
// It is the mutable counterpart to lakegrave: where lakegrave (lakearch) is
// append-only and content-addressed and therefore implements only the base
// Graveyard, schemegrave honors the Ref as a PATH, overwrites in place, and adds
// Delete + List. Put maps to scheme_put (base, §6.4), Get to scheme_get, Delete to
// scheme_delete, List to scheme_list — all through the buffer-too-small retry
// protocol. The richer, path+description-aware verbs (PutStructured, Move,
// SetDescription, Describe) live in the Structured interface below — scheme's own
// interface in its own package, exactly as the graveyard contract invites for a
// backend with its own model.
//
// Built only with `-tags scheme`: it needs cgo and links libscheme_ffi.a. The FFI
// is an in-process embedding within ONE trust zone — this process is the trusted
// layer above it (see scheme_ffi.h).
package schemegrave

/*
#cgo CFLAGS: -I${SRCDIR}/../../../scheme/crates/scheme-ffi/include
#cgo LDFLAGS: ${SRCDIR}/../../../scheme/target/release/libscheme_ffi.a -lpthread -ldl -lm
#include <stdlib.h>
#include "scheme_ffi.h"
*/
import "C"

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/sxty9/prizm/graveyard"
)

// Bestand is an open scheme store exposed as a graveyard backend.
type Bestand struct {
	mu     sync.Mutex
	handle *C.SchemeHandle
}

// Structured is scheme's richer, path- and description-aware surface. The base
// graveyard.Graveyard + Deletable + Listable are the INTERCHANGEABLE seam (what
// makes scheme swappable with lakearch); these methods are scheme-specific and let
// the agent supply a clear path and the mandatory Beschreibung, and read scheme's
// structure-guidance Leitfaden (§9).
type Structured interface {
	graveyard.Graveyard
	// PutStructured stores data at path with the MANDATORY description (§4); an
	// empty/whitespace description is rejected by the substrate.
	PutStructured(ctx context.Context, path, description string, data []byte) (graveyard.Ref, error)
	// Move re-keys a node from one path to another (§6).
	Move(ctx context.Context, from, to graveyard.Ref) error
	// SetDescription sets/replaces a node's description and clears its
	// "undescribed" mark (§4).
	SetDescription(ctx context.Context, path, description string) error
	// Describe returns scheme's structure-guidance Leitfaden (§9): the text telling
	// the agent that the data is clearly structured and must be described that way.
	Describe() string
}

// Compile-time proof of the contract: schemegrave is a Graveyard, is deletable and
// listable (unlike an append-only backend), and offers scheme's richer surface.
var (
	_ graveyard.Graveyard = (*Bestand)(nil)
	_ graveyard.Deletable = (*Bestand)(nil)
	_ graveyard.Listable  = (*Bestand)(nil)
	_ Structured          = (*Bestand)(nil)
)

// Open opens (creating if needed) a scheme store rooted at dir.
func Open(dir string) (*Bestand, error) {
	cdir := C.CString(dir)
	defer C.free(unsafe.Pointer(cdir))
	var h *C.SchemeHandle
	st := C.scheme_open(cdir, C.size_t(len(dir)), &h)
	if st != C.SCHEME_OK {
		return nil, statusErr("open", st)
	}
	b := &Bestand{handle: h}
	// Safety net if a caller forgets Close(); explicit Close() clears this finalizer.
	runtime.SetFinalizer(b, func(b *Bestand) { _ = b.Close() })
	return b, nil
}

// Close releases the store handle. Idempotent here; the FFI forbids a double close,
// which the nil guard prevents.
func (b *Bestand) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return nil
	}
	st := C.scheme_close(b.handle)
	b.handle = nil
	runtime.SetFinalizer(b, nil)
	if st != C.SCHEME_OK {
		return statusErr("close", st)
	}
	return nil
}

// Put stores data at ref (honored as a PATH — scheme is mutable, so a non-empty ref
// overwrites in place). An empty ref lands the datum under the well-known eingang/
// folder with a derived placeholder description (§6.4). The returned Ref is the
// resulting human path. This is the base graveyard seam; PutStructured is the
// intended, described path.
func (b *Bestand) Put(_ context.Context, ref graveyard.Ref, data []byte) (graveyard.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return "", errClosed
	}
	path := string(ref)
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	// Buffer protocol for the returned path (small; 4096 is ample, retry if not).
	buf := C.malloc(4096)
	defer func() { C.free(buf) }()
	outLen := C.size_t(4096)
	st := C.scheme_put(b.handle, cpath, C.size_t(len(path)), dataPtr(data), C.size_t(len(data)),
		(*C.uint8_t)(buf), &outLen)
	if st == C.SCHEME_BUFFER_TOO_SMALL {
		C.free(buf)
		buf = C.malloc(outLen)
		st = C.scheme_put(b.handle, cpath, C.size_t(len(path)), dataPtr(data), C.size_t(len(data)),
			(*C.uint8_t)(buf), &outLen)
	}
	if st != C.SCHEME_OK {
		return "", statusErr("put", st)
	}
	if outLen > maxCLen {
		return "", fmt.Errorf("schemegrave: put: returned path length %d exceeds the C-ABI boundary limit", uint64(outLen))
	}
	return graveyard.Ref(C.GoStringN((*C.char)(buf), C.int(outLen))), nil
}

// Get reads the file at ref (a path). A missing node (or a folder) is found=false
// with no error — absence is a normal outcome (the graveyard contract).
func (b *Bestand) Get(_ context.Context, ref graveyard.Ref) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return nil, false, errClosed
	}
	path := string(ref)
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	buf := C.malloc(4096)
	defer func() { C.free(buf) }()
	outLen := C.size_t(4096)
	st := C.scheme_get(b.handle, cpath, C.size_t(len(path)), (*C.uint8_t)(buf), &outLen)
	if st == C.SCHEME_BUFFER_TOO_SMALL {
		C.free(buf)
		buf = C.malloc(outLen)
		st = C.scheme_get(b.handle, cpath, C.size_t(len(path)), (*C.uint8_t)(buf), &outLen)
	}
	switch st {
	case C.SCHEME_OK:
		if outLen > maxCLen {
			return nil, false, fmt.Errorf("schemegrave: get: payload %d bytes exceeds the C-ABI boundary limit", uint64(outLen))
		}
		return C.GoBytes(buf, C.int(outLen)), true, nil
	case C.SCHEME_NOT_FOUND:
		return nil, false, nil
	default:
		return nil, false, statusErr("get", st)
	}
}

// Delete removes the node at ref (recursively for a folder). Idempotent: an absent
// node is not an error. Implements graveyard.Deletable — scheme is NOT append-only.
func (b *Bestand) Delete(_ context.Context, ref graveyard.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return errClosed
	}
	path := string(ref)
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var removed C.int32_t
	st := C.scheme_delete(b.handle, cpath, C.size_t(len(path)), &removed)
	if st != C.SCHEME_OK {
		return statusErr("delete", st)
	}
	return nil
}

// List enumerates the file paths under prefix, sorted. Implements
// graveyard.Listable — the hierarchy is enumerable.
func (b *Bestand) List(_ context.Context, prefix graveyard.Ref) ([]graveyard.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return nil, errClosed
	}
	p := string(prefix)
	cpre := C.CString(p)
	defer C.free(unsafe.Pointer(cpre))

	bufCap := C.size_t(1 << 16)
	buf := C.malloc(bufCap)
	defer func() { C.free(buf) }()
	outLen := bufCap
	st := C.scheme_list(b.handle, cpre, C.size_t(len(p)), (*C.uint8_t)(buf), &outLen)
	if st == C.SCHEME_BUFFER_TOO_SMALL {
		C.free(buf)
		buf = C.malloc(outLen)
		st = C.scheme_list(b.handle, cpre, C.size_t(len(p)), (*C.uint8_t)(buf), &outLen)
	}
	if st != C.SCHEME_OK {
		return nil, statusErr("list", st)
	}
	if outLen > maxCLen {
		return nil, fmt.Errorf("schemegrave: list: %d bytes exceeds the C-ABI boundary limit", uint64(outLen))
	}
	joined := C.GoStringN((*C.char)(buf), C.int(outLen))
	if joined == "" {
		return []graveyard.Ref{}, nil
	}
	parts := strings.Split(joined, "\n")
	refs := make([]graveyard.Ref, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			refs = append(refs, graveyard.Ref(s))
		}
	}
	return refs, nil
}

// PutStructured stores data at path with the mandatory description (§4). An empty
// description returns an error (the substrate enforces it).
func (b *Bestand) PutStructured(_ context.Context, path, description string, data []byte) (graveyard.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return "", errClosed
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cdesc := C.CString(description)
	defer C.free(unsafe.Pointer(cdesc))
	st := C.scheme_put_structured(b.handle, cpath, C.size_t(len(path)),
		cdesc, C.size_t(len(description)), dataPtr(data), C.size_t(len(data)))
	if st != C.SCHEME_OK {
		return "", statusErr("put_structured", st)
	}
	return graveyard.Ref(path), nil
}

// Move re-keys a node from one path to another (§6).
func (b *Bestand) Move(_ context.Context, from, to graveyard.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return errClosed
	}
	f, t := string(from), string(to)
	cfrom := C.CString(f)
	defer C.free(unsafe.Pointer(cfrom))
	cto := C.CString(t)
	defer C.free(unsafe.Pointer(cto))
	st := C.scheme_move(b.handle, cfrom, C.size_t(len(f)), cto, C.size_t(len(t)))
	if st != C.SCHEME_OK {
		return statusErr("move", st)
	}
	return nil
}

// SetDescription sets/replaces a node's description and clears its "undescribed"
// mark (§4). An empty description is rejected.
func (b *Bestand) SetDescription(_ context.Context, path, description string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle == nil {
		return errClosed
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cdesc := C.CString(description)
	defer C.free(unsafe.Pointer(cdesc))
	st := C.scheme_set_description(b.handle, cpath, C.size_t(len(path)), cdesc, C.size_t(len(description)))
	if st != C.SCHEME_OK {
		return statusErr("set_description", st)
	}
	return nil
}

// Describe returns scheme's structure-guidance Leitfaden (§9). It is static, so it
// needs no open handle and never fails; on any unexpected FFI condition it returns
// an empty string (the caller simply injects nothing).
func (b *Bestand) Describe() string {
	buf := C.malloc(4096)
	defer func() { C.free(buf) }()
	outLen := C.size_t(4096)
	st := C.scheme_describe((*C.uint8_t)(buf), &outLen)
	if st == C.SCHEME_BUFFER_TOO_SMALL {
		C.free(buf)
		buf = C.malloc(outLen)
		st = C.scheme_describe((*C.uint8_t)(buf), &outLen)
	}
	if st != C.SCHEME_OK || outLen > maxCLen {
		return ""
	}
	return C.GoStringN((*C.char)(buf), C.int(outLen))
}

var errClosed = fmt.Errorf("schemegrave: store closed")

// maxCLen is the largest length safely passed to the cgo GoBytes/GoStringN
// builtins, whose length argument is a C.int (int32 on every Go platform). A
// length at/above 2^31 would narrow to a NEGATIVE int32 and trigger a FATAL,
// unrecoverable runtime panic ("length out of range"). We return an explicit error
// instead. Payloads this large are pathological for an in-process boundary.
const maxCLen = C.size_t(1<<31 - 1)

// dataPtr returns a C pointer to the Go byte slice (nil for empty). scheme copies
// the bytes during the call and never retains the pointer, so passing the Go
// pointer for the duration of the call is sound.
func dataPtr(data []byte) *C.uint8_t {
	if len(data) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&data[0]))
}

func statusErr(op string, st C.SchemeStatus) error {
	return fmt.Errorf("schemegrave: %s: scheme status %d", op, int(st))
}
