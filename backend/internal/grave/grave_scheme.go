//go:build scheme

package grave

import (
	"github.com/sxty9/aigentic/graveyard/schemegrave"
	"github.com/sxty9/prizm/graveyard"
)

// openScheme opens a scheme-backed graveyard (mutable, path-addressed, clearly
// structured). dir defaults to the service's state dir.
func openScheme(dir string) (graveyard.Graveyard, error) {
	if dir == "" {
		dir = "/var/lib/aigentic/scheme"
	}
	return schemegrave.Open(dir)
}
