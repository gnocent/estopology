package query

import (
	"container/list"
	"sync"

	"estopo/internal/model"
)

// lru is a small LRU cache of reconstructed state halves.
type lru struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element
	order *list.List
}

type lruEntry struct {
	key   string
	state *model.State
}

func newLRU(max int) *lru {
	return &lru{max: max, items: map[string]*list.Element{}, order: list.New()}
}

func (c *lru) get(key string) *model.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*lruEntry).state
	}
	return nil
}

func (c *lru) put(key string, s *model.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry).state = s
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry{key: key, state: s})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*lruEntry).key)
	}
}

func (c *lru) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]*list.Element{}
	c.order.Init()
}
