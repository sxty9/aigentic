//go:build lakearch

package grave

import (
	"github.com/sxty9/aigentic/graveyard/lakegrave"
	"github.com/sxty9/prizm/graveyard"
)

// openLakearch opens a lakearch-backed graveyard. dir defaults to the service's state dir.
func openLakearch(dir string) (graveyard.Graveyard, error) {
	if dir == "" {
		dir = "/var/lib/aigentic/lakearch"
	}
	return lakegrave.Open(dir)
}
