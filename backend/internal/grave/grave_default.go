//go:build !lakearch

package grave

import (
	"errors"

	"github.com/sxty9/prizm/graveyard"
)

// openLakearch is the no-op stand-in compiled when the `lakearch` build tag is absent.
func openLakearch(string) (graveyard.Graveyard, error) {
	return nil, errors.New("grave: aigentic was built without lakearch support (rebuild with -tags lakearch)")
}
