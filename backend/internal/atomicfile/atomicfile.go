// Package atomicfile is the single, shared implementation of an atomic file write for the
// aigentic daemon's persistent data pools (the per-user chat blob and the per-user/global
// credentials).
//
// It realizes the Holistic "Atomare Zugriffe" axiom for on-disk storage: a write is published as
// one indivisible step. Content is streamed into a uniquely named temp file in the TARGET's own
// directory and then renamed over the target; because a same-filesystem rename is atomic, a
// concurrent reader observes either the whole previous file or the whole new one — never a torn or
// half-written value, and never an observable intermediate state. On any error the temp file is
// removed, so a failed write leaves neither a partial target nor a stray temp.
//
// Being ONE primitive that every pool reuses (Single Source of Truth) means the atomicity guarantee
// is written and tested once and inherited everywhere, instead of re-implemented — and re-drifted —
// per store.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write publishes content to path atomically at the given permission bits, creating the parent
// directory (0700) if it does not exist. perm is applied explicitly rather than left to the umask,
// so a credential file is always exactly its declared mode (e.g. 0600) regardless of the process
// umask.
func Write(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The temp file MUST share the target's directory so the final rename stays on one filesystem
	// (a cross-device rename is not atomic and would fail).
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Past this point every error path must remove the temp so a failure never leaves a stray.
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
