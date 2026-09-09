package embed

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
)

// CachingEmbedder memoizes embeddings by normalized text.
//
// This is the single largest saving on the hot path and it is independent of
// which model sits underneath. A health endpoint returns the same payload on
// every check; once timestamps and counters are masked away, consecutive bodies
// are byte-identical, so the great majority of drift checks never need to
// embed anything at all.
//
// Entries are keyed on a hash rather than the text itself, so a cache of
// thousands of entries costs kilobytes and never holds a response body in
// memory longer than the call that produced it.
type CachingEmbedder struct {
	inner    Embedder
	capacity int

	mu      sync.Mutex
	entries map[[32]byte]*list.Element
	order   *list.List

	hits   atomic.Uint64
	misses atomic.Uint64
}

type cacheEntry struct {
	key    [32]byte
	vector Vector
}

// NewCachingEmbedder wraps an embedder with an LRU cache.
// A capacity of zero or less disables caching and returns the inner embedder.
func NewCachingEmbedder(inner Embedder, capacity int) Embedder {
	if capacity <= 0 {
		return inner
	}

	return &CachingEmbedder{
		inner:    inner,
		capacity: capacity,
		entries:  make(map[[32]byte]*list.Element, capacity),
		order:    list.New(),
	}
}

// Space implements Embedder.
func (c *CachingEmbedder) Space() string { return c.inner.Space() }

// Dimensions implements Embedder.
func (c *CachingEmbedder) Dimensions() int { return c.inner.Dimensions() }

// Close implements Embedder.
func (c *CachingEmbedder) Close() error { return c.inner.Close() }

// Stats reports cache hits and misses, surfaced as Prometheus counters.
func (c *CachingEmbedder) Stats() (hits, misses uint64) {
	return c.hits.Load(), c.misses.Load()
}

// Embed implements Embedder, resolving what it can from cache and delegating
// only the genuinely new texts.
func (c *CachingEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	vectors := make([]Vector, len(texts))
	keys := make([][32]byte, len(texts))

	// Indexes of texts that were not cached, and the texts themselves.
	var missIndexes []int
	var missTexts []string

	for index, text := range texts {
		key := sha256.Sum256([]byte(text))
		keys[index] = key

		if vector, found := c.lookup(key); found {
			vectors[index] = vector
			c.hits.Add(1)
			continue
		}

		c.misses.Add(1)
		missIndexes = append(missIndexes, index)
		missTexts = append(missTexts, text)
	}

	if len(missTexts) == 0 {
		return vectors, nil
	}

	computed, err := c.inner.Embed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	if len(computed) != len(missTexts) {
		return nil, errShortResult{want: len(missTexts), got: len(computed)}
	}

	for position, index := range missIndexes {
		vectors[index] = computed[position]
		c.store(keys[index], computed[position])
	}

	return vectors, nil
}

func (c *CachingEmbedder) lookup(key [32]byte) (Vector, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, found := c.entries[key]
	if !found {
		return Vector{}, false
	}

	c.order.MoveToFront(element)
	return element.Value.(*cacheEntry).vector, true
}

func (c *CachingEmbedder) store(key [32]byte, vector Vector) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.entries[key]; found {
		c.order.MoveToFront(element)
		element.Value.(*cacheEntry).vector = vector
		return
	}

	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, vector: vector})

	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cacheEntry).key)
	}
}

// errShortResult guards against an embedder returning the wrong number of
// vectors, which would otherwise misalign every result with its input.
type errShortResult struct{ want, got int }

func (e errShortResult) Error() string {
	return fmt.Sprintf("embed: embedder returned %d vectors for %d texts", e.got, e.want)
}
