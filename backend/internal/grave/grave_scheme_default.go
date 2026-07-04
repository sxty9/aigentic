//go:build !scheme

package grave

import (
	"errors"

	"github.com/sxty9/prizm/graveyard"
)

// openScheme is the no-op stand-in compiled when the `scheme` build tag is absent.
func openScheme(string) (graveyard.Graveyard, error) {
	return nil, errors.New("grave: aigentic was built without scheme support (rebuild with -tags scheme)")
}
