// Package clock provides injectable sources of UTC time.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

// RealClock reads the system clock in UTC.
type RealClock struct{}

// Now returns the current system time in UTC.
func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

// FakeClock is a concurrency-safe controllable clock.
type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFakeClock returns a fake clock initialized to now, normalized to UTC.
func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now.UTC()}
}

// Now returns the fake clock's current time.
func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the clock by d and returns the resulting time.
func (c *FakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}
