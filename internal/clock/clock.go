// Package clock provides the injectable time source used by the collector,
// the ingestion and the retention (SPEC §4): real time in production, a
// manual clock for deterministic tests and backfills, a scaled clock for
// accelerated simulations.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the time source. All times are UTC.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives the clock's time once d has
	// elapsed on this clock.
	After(d time.Duration) <-chan time.Time
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

func (Real) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	time.AfterFunc(d, func() { ch <- time.Now().UTC() })
	return ch
}

// Scaled runs Factor times faster than the wall clock, starting at Start.
type Scaled struct {
	start  time.Time
	origin time.Time
	factor float64
}

// NewScaled returns a clock that reads start now and advances factor times
// faster than real time.
func NewScaled(start time.Time, factor float64) *Scaled {
	if factor <= 0 {
		factor = 1
	}
	return &Scaled{start: start.UTC(), origin: time.Now(), factor: factor}
}

func (s *Scaled) Now() time.Time {
	elapsed := time.Duration(float64(time.Since(s.origin)) * s.factor)
	return s.start.Add(elapsed)
}

func (s *Scaled) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	real := time.Duration(float64(d) / s.factor)
	time.AfterFunc(real, func() { ch <- s.Now() })
	return ch
}

// Manual only moves when told to. Waiters registered with After fire when
// Advance or Set reaches their deadline.
type Manual struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

// NewManual returns a manual clock set to start.
func NewManual(start time.Time) *Manual {
	m := &Manual{now: start.UTC()}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *Manual) After(d time.Duration) <-chan time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := m.now.Add(d)
	if d <= 0 {
		ch <- m.now
		return ch
	}
	m.waiters = append(m.waiters, waiter{at: at, ch: ch})
	sort.SliceStable(m.waiters, func(i, j int) bool { return m.waiters[i].at.Before(m.waiters[j].at) })
	m.cond.Broadcast()
	return ch
}

// Advance moves the clock forward by d and fires the due waiters.
func (m *Manual) Advance(d time.Duration) { m.Set(m.Now().Add(d)) }

// Set moves the clock to t (never backwards) and fires the due waiters.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t = t.UTC()
	if t.After(m.now) {
		m.now = t
	}
	i := 0
	for ; i < len(m.waiters) && !m.waiters[i].at.After(m.now); i++ {
		m.waiters[i].ch <- m.now
	}
	m.waiters = m.waiters[i:]
}

// NextDeadline returns the earliest pending waiter deadline, if any. A
// backfill driver jumps straight to it instead of sleeping.
func (m *Manual) NextDeadline() (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.waiters) == 0 {
		return time.Time{}, false
	}
	return m.waiters[0].at, true
}

// BlockUntil waits until at least n waiters are pending, so tests can be
// sure a goroutine is parked on the clock before advancing it.
func (m *Manual) BlockUntil(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.waiters) < n {
		m.cond.Wait()
	}
}
