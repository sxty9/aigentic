// Package grave selects the graveyard substrate (G) for the aigentic daemon: the
// in-memory stub by default, or lakearch when built with -tags lakearch. Keeping the
// lakearch (cgo) backend behind a build tag means the default build stays pure-Go and
// needs neither a C toolchain nor the lakearch library present.
package grave

import (
	"fmt"

	"github.com/sxty9/prizm/graveyard"
)

// Open returns a graveyard for the named backend. "memory" (the default) is the in-memory
// stub; "lakearch" is the content-addressed, append-only substrate (needs the `lakearch`
// build tag); "scheme" is the mutable, path-addressed, clearly-structured filesystem-tree
// substrate (needs the `scheme` build tag). Both native substrates require a writable dir.
func Open(kind, dir string) (graveyard.Graveyard, error) {
	switch kind {
	case "", "memory":
		return graveyard.NewMemory(), nil
	case "lakearch":
		return openLakearch(dir)
	case "scheme":
		return openScheme(dir)
	default:
		return nil, fmt.Errorf("grave: unknown backend %q (want memory|lakearch|scheme)", kind)
	}
}
