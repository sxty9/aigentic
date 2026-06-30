package aigentic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sxty9/prizm/prizm"
)

// assemble turns a request into the final prompt the engine sees. It reads Request.Paths
// (confined under <root>/<Subject>), stores each included file's bytes in the graveyard
// for content-addressed provenance, and concatenates a bounded context block ahead of the
// prompt — respecting the byte budget (the input half of the token-overusage guard).
//
// Path reading is the graveyard's (G) job and lives here, shared by all three leaves so a
// choose-forwarded request assembles identically regardless of which leaf runs. The
// classifier deliberately does NOT call assemble (its estimate must stay cheap).
//
// It never hard-fails on an individual path: a missing, denied, binary, oversized or
// over-budget path is recorded as a skipped ContextItem and skipped. Only a graveyard
// write error aborts (it signals a substrate fault, not a bad request).
func assemble(ctx context.Context, env prizm.Env, in Request, lim Limits) (prompt string, items []ContextItem, truncated bool, err error) {
	budget := lim.MaxContextBytes
	if budget <= 0 {
		budget = DefaultMaxContextBytes
	}
	scope := subjectScope(lim.ContextRoot, env.Header.Subject)

	var b strings.Builder
	used := 0
	for _, p := range in.Paths {
		files, denied := resolvePath(scope, p)
		if denied != "" {
			items = append(items, ContextItem{Path: p, Skipped: denied})
			continue
		}
		for _, f := range files {
			if used >= budget {
				items = append(items, ContextItem{Path: f.rel, Skipped: "budget"})
				truncated = true
				continue
			}
			data, skip := readForContext(f.abs, budget-used)
			if skip != "" {
				items = append(items, ContextItem{Path: f.rel, Skipped: skip})
				if skip == "budget" {
					truncated = true
				}
				continue
			}
			ref, perr := env.Grave.Put(ctx, "", data)
			if perr != nil {
				return "", nil, false, fmt.Errorf("graveyard put %q: %w", f.rel, perr)
			}
			fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n", f.rel, data)
			used += len(data)
			items = append(items, ContextItem{Path: f.rel, Ref: ref, Bytes: len(data)})
		}
	}

	prompt = composePrompt(b.String(), in)
	return prompt, items, truncated, nil
}

// composePrompt assembles the final prompt: an optional context block, the instruction,
// and an output-format hint.
func composePrompt(contextBlock string, in Request) string {
	var b strings.Builder
	if contextBlock != "" {
		b.WriteString("Context files:\n")
		b.WriteString(contextBlock)
		b.WriteString("\n")
	}
	b.WriteString(in.Prompt)
	if in.OutputFormat != "" && in.OutputFormat != "text" {
		fmt.Fprintf(&b, "\n\nRespond in %s format.", in.OutputFormat)
	}
	return b.String()
}

// subjectScope is the confinement root for a caller: <root>/<subject>. Subject is the
// server-stamped holistic identity (api.go sets it; never the wire), so callers are
// isolated from each other. An empty subject scopes to the root itself.
func subjectScope(root, subject string) string {
	if root == "" {
		root = DefaultContextRoot
	}
	// Clean the subject to a single path element (defence in depth; Subject is trusted but
	// cheap to harden).
	subject = strings.TrimSpace(subject)
	subject = strings.ReplaceAll(subject, string(os.PathSeparator), "")
	subject = strings.ReplaceAll(subject, "..", "")
	if subject == "" {
		return filepath.Clean(root)
	}
	return filepath.Join(root, subject)
}

type contextFile struct {
	abs string // absolute path on disk
	rel string // path relative to the scope root (what the model and provenance see)
}

// resolvePath confines p to scope and expands a directory to its files. It returns either
// the matched files or a non-empty skip reason ("denied" for an escape/unresolvable path,
// "missing" for a non-existent one).
func resolvePath(scope, p string) (files []contextFile, skip string) {
	// Resolve p relative to the scope (absolute p is re-rooted under the scope too).
	joined := filepath.Join(scope, strings.TrimPrefix(filepath.Clean(p), string(os.PathSeparator)))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return nil, "denied"
	}
	// Defeat symlink escape: the resolved real path must stay within the scope.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "missing"
		}
		return nil, "denied"
	}
	if !withinScope(scope, real) {
		return nil, "denied"
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, "missing"
	}
	if !info.IsDir() {
		return []contextFile{{abs: real, rel: relTo(scope, real)}}, ""
	}
	// Directory: walk, skipping noise and anything that escapes the scope via symlink.
	_ = filepath.WalkDir(real, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			if d != nil && d.IsDir() && isNoiseDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isNoiseFile(d.Name()) {
			return nil
		}
		rp, err := filepath.EvalSymlinks(path)
		if err != nil || !withinScope(scope, rp) {
			return nil
		}
		files = append(files, contextFile{abs: rp, rel: relTo(scope, rp)})
		return nil
	})
	if len(files) == 0 {
		return nil, "missing"
	}
	return files, ""
}

// readForContext reads up to remaining bytes of a file, rejecting oversized or binary
// content with a skip reason.
func readForContext(abs string, remaining int) (data []byte, skip string) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, "missing"
	}
	if info.Size() > maxFileBytes {
		return nil, "too-large"
	}
	if remaining <= 0 {
		return nil, "budget"
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, "denied"
	}
	if isBinary(b) {
		return nil, "binary"
	}
	if len(b) > remaining {
		// keep it whole-file: defer to the next request rather than feed a half file.
		return nil, "budget"
	}
	return b, ""
}

func withinScope(scope, target string) bool {
	scope = filepath.Clean(scope)
	if target == scope {
		return true
	}
	return strings.HasPrefix(target, scope+string(os.PathSeparator))
}

func relTo(scope, target string) string {
	if r, err := filepath.Rel(scope, target); err == nil {
		return r
	}
	return target
}

func isNoiseDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".svn", "vendor", "target":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func isNoiseFile(name string) bool { return strings.HasPrefix(name, ".") }

// isBinary sniffs the first chunk for a NUL byte or invalid UTF-8 — good enough to keep
// non-text out of the prompt.
func isBinary(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	head := b[:n]
	for _, c := range head {
		if c == 0 {
			return true
		}
	}
	return !utf8.Valid(head)
}
