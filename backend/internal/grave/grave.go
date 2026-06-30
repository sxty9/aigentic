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
// stub; "lakearch" is the content-addressed, append-only substrate and requires both the
// `lakearch` build tag and a writable kernel directory dir.
func Open(kind, dir string) (graveyard.Graveyard, error) {
	switch kind {
	case "", "memory":
		return graveyard.NewMemory(), nil
	case "lakearch":
		return openLakearch(dir)
	default:
		return nil, fmt.Errorf("grave: unknown backend %q (want memory|lakearch)", kind)
	}
}
