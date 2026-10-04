package clock

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

func TestManualAfterFiresOnAdvance(t *testing.T) {
	m := NewManual(t0)
	ch := m.After(5 * time.Minute)
	m.Advance(4 * time.Minute)
	select {
	case <-ch:
		t.Fatal("fired too early")
	default:
	}
	m.Advance(time.Minute)
	select {
	case got := <-ch:
		if !got.Equal(t0.Add(5 * time.Minute)) {
			t.Errorf("fired at %v", got)
		}
	default:
		t.Fatal("did not fire")
	}
}

func TestManualNeverGoesBackwards(t *testing.T) {
	m := NewManual(t0)
	m.Set(t0.Add(-time.Hour))
	if !m.Now().Equal(t0) {
		t.Errorf("clock went backwards: %v", m.Now())
	}
}

func TestManualNextDeadlineAndBlockUntil(t *testing.T) {
	m := NewManual(t0)
	done := make(chan struct{})
	go func() {
		<-m.After(10 * time.Minute)
		close(done)
	}()
	m.BlockUntil(1)
	m.After(time.Minute)
	at, ok := m.NextDeadline()
	if !ok || !at.Equal(t0.Add(time.Minute)) {
		t.Fatalf("NextDeadline = %v, %v", at, ok)
	}
	m.Advance(10 * time.Minute)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiter not released")
	}
	if _, ok := m.NextDeadline(); ok {
		t.Error("waiters left after advancing past all deadlines")
	}
}

func TestScaled(t *testing.T) {
	s := NewScaled(t0, 3600) // one hour per second
	got := <-s.After(time.Hour)
	if d := got.Sub(t0); d < time.Hour || d > 3*time.Hour {
		t.Errorf("scaled After fired at +%v", d)
	}
}
