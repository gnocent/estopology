package store

import (
	"strings"
	"testing"
	"time"

	"estopo/internal/sim"
)

// B4: a node holding primaries leaves; the replicas elsewhere are promoted,
// then new replicas are allocated after the delayed timeout.
func TestShardLossPromotionReassignment(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	victim := "small-hot-dc1-m01-1"
	var primaries, copies int
	for _, ix := range e.c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.Node != nil && cp.Node.Name == victim {
					copies++
					if cp.Primary {
						primaries++
					}
				}
			}
		}
	}
	if primaries == 0 {
		t.Fatal("test assumption: the victim holds primaries")
	}
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	e.full(10 * time.Minute) // after the 1 min delayed timeout: loss, promotion and reallocation are all visible
	lost := e.events(0, "shard_lost")
	promoted := e.events(0, "shard_promoted")
	if len(lost) != copies {
		t.Errorf("%d shard_lost, want %d", len(lost), copies)
	}
	for _, ev := range lost {
		if !strings.Contains(ev, `"node_left":true`) || !strings.Contains(ev, `"node":"`+victim+`"`) {
			t.Errorf("lost event %s", ev)
		}
	}
	if len(promoted) != primaries {
		t.Errorf("%d shard_promoted, want %d", len(promoted), primaries)
	}
	// Lost primaries whose replica was promoted are not critical.
	if n := e.count(0, `SELECT count(*) FROM event WHERE type = 'shard_lost' AND severity = 1`); n != 0 {
		t.Errorf("%d critical losses although every primary had a replica", n)
	}
	// By +10 min the replicas were reallocated (delay 1 min): assigned events,
	// no copy left unassigned.
	if n := len(e.events(0, "shard_assigned")); n != copies {
		t.Errorf("%d shard_assigned, want %d", n, copies)
	}
	if n := e.count(0, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL AND node_id IS NULL`); n != 0 {
		t.Errorf("%d copies still unassigned", n)
	}
}

// Losses seen before reallocation: unassigned copies grouped by the node left.
func TestUnassignedAfterNodeLeft(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	victim := "small-hot-dc2-m01-1"
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	e.full(30 * time.Second) // before the 1 min delayed timeout
	un := e.events(0, "shard_unassigned")
	if len(un) == 0 {
		t.Fatal("no shard_unassigned")
	}
	if n := e.count(0, `SELECT count(*) FROM event WHERE type = 'shard_unassigned' AND group_ref = ? AND exact_ts IS NOT NULL`, victim); n != len(un) {
		t.Errorf("%d of %d unassigned events grouped by %s with an exact date", n, len(un), victim)
	}
}

// B5: a deliberately deleted index produces no shard event.
func TestDeletedIndexNoShardEvents(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.c.Do(func(c *sim.Cluster) { c.DeleteIndex(c.Indices[3].Name) })
	e.full(10 * time.Minute)
	if n := e.count(0, `SELECT count(*) FROM event WHERE type LIKE 'shard_%'`); n != 0 {
		t.Errorf("%d shard events for a deleted index", n)
	}
	if len(e.events(0, "index_deleted")) != 1 {
		t.Error("no index_deleted")
	}
}

// Tier moves after midnight relocate copies: shard_relocated events.
func TestRelocationEvents(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	// t0 is 12:00; midnight is +12h. Capture every 30 min until +16h.
	for d := 30 * time.Minute; d <= 16*time.Hour; d += 30 * time.Minute {
		e.full(d)
	}
	rel := e.events(0, "shard_relocated")
	if len(rel) == 0 {
		t.Fatal("no shard_relocated after tier moves")
	}
	if n := e.count(0, `SELECT count(*) FROM event WHERE type IN ('shard_lost', 'shard_promoted')`); n != 0 {
		t.Errorf("%d loss events without any node leaving", n)
	}
	for _, ev := range rel[:1] {
		if !strings.Contains(ev, `"from":`) || !strings.Contains(ev, `"to":`) {
			t.Errorf("relocation payload %s", ev)
		}
	}
}
