package main

import (
	"container/list"
	"sync"
)

const (
	translateCacheCapacity   = 64
	translateCacheByteBudget = 2 * 1024 * 1024
)

// translateCache is a small mutex-protected LRU keyed by
// "source|target|text", holding finished translations. Strings are immutable
// in Go, so returning a TranslationResult copies the header but shares the
// backing data safely.
type translateCache struct {
	mu     sync.Mutex
	cap    int
	budget int
	used   int
	ll     *list.List
	index  map[string]*list.Element
}

var translateCacheInstance = &translateCache{
	cap:    translateCacheCapacity,
	budget: translateCacheByteBudget,
	ll:     list.New(),
	index:  make(map[string]*list.Element),
}

type translateCacheEntry struct {
	key    string
	result TranslationResult
	bytes  int
}

func translateCacheEntryBytes(key string, r TranslationResult) int {
	return len(key) + len(r.TranslatedText) + len(r.DetectedSource) + 64
}

// cachedTranslate returns a copy of a cached result, if present, and moves
// the entry to the front of the LRU.
func cachedTranslate(key string) (TranslationResult, bool) {
	c := translateCacheInstance
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.index[key]
	if !ok {
		return TranslationResult{}, false
	}
	c.ll.MoveToFront(el)
	e := el.Value.(*translateCacheEntry)
	// Return by value so callers cannot mutate cached state.
	return e.result, true
}

// storeTranslate inserts or replaces an entry, evicting from the tail until
// both the count and byte budgets are respected.
func storeTranslate(key string, r TranslationResult) {
	c := translateCacheInstance
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*translateCacheEntry)
		c.used -= e.bytes
		e.result = r
		e.bytes = translateCacheEntryBytes(key, r)
		c.used += e.bytes
		evictTranslateLocked(c)
		return
	}

	entry := &translateCacheEntry{
		key:    key,
		result: r,
		bytes:  translateCacheEntryBytes(key, r),
	}
	el := c.ll.PushFront(entry)
	c.index[key] = el
	c.used += entry.bytes
	evictTranslateLocked(c)
}

func evictTranslateLocked(c *translateCache) {
	for c.ll.Len() > c.cap || c.used > c.budget {
		back := c.ll.Back()
		if back == nil {
			break
		}
		e := back.Value.(*translateCacheEntry)
		c.ll.Remove(back)
		delete(c.index, e.key)
		c.used -= e.bytes
	}
}
