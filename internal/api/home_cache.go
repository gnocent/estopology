package api

import (
	"sync"
	"time"
)

// homeTTL bounds how long a home screen summary is reused.
const homeTTL = time.Minute

// homeCache keeps the computed part of the home screen summary of each
// cluster (one entry per cluster), so that the home page does not rebuild
// the state of every cluster at each visit.
type homeCache struct {
	mu    sync.Mutex
	items map[string]homeEntry // by cluster
}

type homeEntry struct {
	key   string
	atMs  int64
	value ClusterSummary
}

func (c *homeCache) get(key string, nowMs int64) (ClusterSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.items {
		if e.key == key && nowMs-e.atMs < homeTTL.Milliseconds() {
			return e.value, true
		}
	}
	return ClusterSummary{}, false
}

func (c *homeCache) put(cluster, key string, nowMs int64, v ClusterSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]homeEntry{}
	}
	c.items[cluster] = homeEntry{key: key, atMs: nowMs, value: v}
}
