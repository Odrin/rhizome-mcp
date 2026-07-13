package ids_test

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/ids"
)

func TestGeneratorUsesInjectedTime(t *testing.T) {
	instant := time.Date(2026, 7, 13, 12, 34, 56, 789_000_000, time.UTC)
	generator, err := ids.NewGenerator(clock.NewFakeClock(instant), rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}

	value, err := generator.New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	parsed, err := ids.ParseStrict(value)
	if err != nil {
		t.Fatalf("ParseStrict(%q) error = %v", value, err)
	}
	if got, want := parsed.Time(), ulid.Timestamp(instant); got != want {
		t.Fatalf("ULID timestamp = %d, want %d", got, want)
	}
}

func TestParseStrict(t *testing.T) {
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Unix(1, 0)), rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	valid, err := generator.New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "canonical", value: valid, valid: true},
		{name: "lowercase", value: strings.ToLower(valid)},
		{name: "too short", value: valid[:25]},
		{name: "invalid alphabet", value: valid[:25] + "I"},
		{name: "empty", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ids.ParseStrict(tt.value)
			if tt.valid && err != nil {
				t.Fatalf("ParseStrict() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ids.ErrInvalidULID) {
				t.Fatalf("ParseStrict() error = %v, want ErrInvalidULID", err)
			}
		})
	}
}

func TestGeneratorConcurrentUniqueness(t *testing.T) {
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Unix(1_700_000_000, 0)), rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}

	const count = 1000
	values := make(chan string, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	wg.Add(count)
	for range count {
		go func() {
			defer wg.Done()
			value, err := generator.New()
			if err != nil {
				errs <- err
				return
			}
			values <- value
		}()
	}
	wg.Wait()
	close(values)
	close(errs)

	for err := range errs {
		t.Errorf("New() error = %v", err)
	}
	seen := make(map[string]struct{}, count)
	for value := range values {
		if _, err := ids.ParseStrict(value); err != nil {
			t.Errorf("generated invalid ULID %q: %v", value, err)
		}
		if _, exists := seen[value]; exists {
			t.Errorf("duplicate ULID %q", value)
		}
		seen[value] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("generated %d unique ULIDs, want %d", len(seen), count)
	}
}
