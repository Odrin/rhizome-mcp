package clock_test

import (
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
)

func TestFakeClockAdvanceAndUTC(t *testing.T) {
	initial := time.Date(2026, 7, 13, 10, 0, 0, 0, time.FixedZone("test", 2*60*60))
	fake := clock.NewFakeClock(initial)

	wantInitial := initial.UTC()
	if got := fake.Now(); !got.Equal(wantInitial) || got.Location() != time.UTC {
		t.Fatalf("Now() = %v in %v, want %v in UTC", got, got.Location(), wantInitial)
	}

	wantAdvanced := wantInitial.Add(90 * time.Second)
	if got := fake.Advance(90 * time.Second); !got.Equal(wantAdvanced) {
		t.Fatalf("Advance() = %v, want %v", got, wantAdvanced)
	}
	if got := fake.Now(); !got.Equal(wantAdvanced) {
		t.Fatalf("Now() after Advance = %v, want %v", got, wantAdvanced)
	}
}

func TestFakeClockConcurrentUse(t *testing.T) {
	fake := clock.NewFakeClock(time.Unix(0, 0))
	const workers = 20

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			fake.Advance(time.Second)
			_ = fake.Now()
		}()
	}
	wg.Wait()

	if got, want := fake.Now(), time.Unix(workers, 0).UTC(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}
