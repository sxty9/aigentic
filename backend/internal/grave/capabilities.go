package grave

import (
	"context"

	"github.com/sxty9/prizm/graveyard"
)

// Structured is the richer, path- and description-aware surface a mutable substrate may offer
// (scheme does). It is declared here, tag-neutral, so the HTTP layer can type-assert for it
// without importing the cgo-only schemegrave package; Go's structural typing matches
// schemegrave.Bestand, which has exactly these methods. A memory or append-only backend does not
// implement it, and the graveyard endpoints then report the capability as absent.
type Structured interface {
	PutStructured(ctx context.Context, path, description string, data []byte) (graveyard.Ref, error)
	Move(ctx context.Context, from, to graveyard.Ref) error
	SetDescription(ctx context.Context, path, description string) error
}
