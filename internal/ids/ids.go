// Package ids generates and validates ULID identifiers.
package ids

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/clock"
)

var (
	// ErrInvalidULID identifies a non-canonical or otherwise invalid ULID.
	ErrInvalidULID = errors.New("invalid ULID")
	// ErrMissingDependency identifies an absent generator dependency.
	ErrMissingDependency = errors.New("missing ID generator dependency")
)

// Generator creates ULIDs from injected time and entropy. Calls to New are safe
// for concurrent use, including when the injected entropy reader is not.
type Generator struct {
	clock   clock.Clock
	entropy io.Reader
	mu      sync.Mutex
}

// NewGenerator constructs a Generator without consulting system time or global entropy.
func NewGenerator(source clock.Clock, entropy io.Reader) (*Generator, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: clock", ErrMissingDependency)
	}
	if entropy == nil {
		return nil, fmt.Errorf("%w: entropy", ErrMissingDependency)
	}
	return &Generator{clock: source, entropy: entropy}, nil
}

// New returns a canonical ULID string using the injected clock and entropy.
func (g *Generator) New() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	id, err := ulid.New(ulid.Timestamp(g.clock.Now()), g.entropy)
	if err != nil {
		return "", fmt.Errorf("generate ULID: %w", err)
	}
	return id.String(), nil
}

// ParseStrict parses only canonical, uppercase, 26-character ULIDs.
func ParseStrict(value string) (ulid.ULID, error) {
	id, err := ulid.ParseStrict(value)
	if err != nil || id.String() != value {
		if err == nil {
			err = errors.New("non-canonical representation")
		}
		return ulid.ULID{}, fmt.Errorf("%w: %v", ErrInvalidULID, err)
	}
	return id, nil
}
