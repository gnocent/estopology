package auth

import (
	"sync"
	"time"

	"estopo/internal/clock"
)

// Limiter throttles login attempts (SPEC §10): after `free` failures for a
// key (user name or client address) within `window`, the next attempts are
// refused for an exponentially growing delay (capped at `maxBlock`). A
// success clears the key.
type Limiter struct {
	clock    clock.Clock
	free     int
	window   time.Duration
	maxBlock time.Duration

	mu   sync.Mutex
	keys map[string]*attempts
}

type attempts struct {
	failures int
	first    time.Time
	until    time.Time
}

// NewLimiter returns a limiter with the defaults: 5 free failures in 15
// minutes, blocks from 2 s doubling up to 15 minutes.
func NewLimiter(clk clock.Clock) *Limiter {
	return &Limiter{clock: clk, free: 5, window: 15 * time.Minute, maxBlock: 15 * time.Minute, keys: map[string]*attempts{}}
}

// Allowed reports whether every key may try now; when not, it returns how
// long to wait.
func (l *Limiter) Allowed(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	wait := l.wait(l.clock.Now(), keys)
	return wait == 0, wait
}

func (l *Limiter) wait(now time.Time, keys []string) time.Duration {
	var wait time.Duration
	for _, k := range keys {
		if a := l.keys[k]; a != nil && now.Before(a.until) {
			wait = max(wait, a.until.Sub(now))
		}
	}
	return wait
}

// Reserve is Allowed and Failure in one step: the attempt is counted before
// the password is checked, so that parallel attempts cannot exceed the free
// quota. The caller clears the keys with Success, or gives the attempt back
// with Release when no password was checked after all.
func (l *Limiter) Reserve(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if wait := l.wait(now, keys); wait > 0 {
		return false, wait
	}
	l.fail(now, keys)
	return true, 0
}

// Release gives back an attempt counted by Reserve.
func (l *Limiter) Release(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		a := l.keys[k]
		if a == nil {
			continue
		}
		if a.failures--; a.failures <= 0 {
			delete(l.keys, k)
		} else if a.failures < l.free {
			a.until = time.Time{}
		}
	}
}

// Failure records a failed attempt for every key.
func (l *Limiter) Failure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail(l.clock.Now(), keys)
}

func (l *Limiter) fail(now time.Time, keys []string) {
	for _, k := range keys {
		a := l.keys[k]
		if a == nil || now.Sub(a.first) > l.window {
			a = &attempts{first: now}
			l.keys[k] = a
		}
		a.failures++
		if over := a.failures - l.free + 1; over > 0 { // the (free+1)th attempt waits
			d := 2 * time.Second << min(over-1, 20)
			a.until = now.Add(min(d, l.maxBlock))
		}
	}
	// Forget old entries (bounded memory under a password-spraying attack).
	if len(l.keys) > 10000 {
		for k, a := range l.keys {
			if now.Sub(a.first) > l.window && now.After(a.until) {
				delete(l.keys, k)
			}
		}
	}
}

// Success clears the keys.
func (l *Limiter) Success(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.keys, k)
	}
}
