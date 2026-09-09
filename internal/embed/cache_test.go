package embed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// countingEmbedder records how much work actually reached the model.
type countingEmbedder struct {
	mu       sync.Mutex
	calls    int
	texts    int
	failWith error
}

func (c *countingEmbedder) Space() string   { return SpacePotion }
func (c *countingEmbedder) Dimensions() int { return 2 }
func (c *countingEmbedder) Close() error    { return nil }

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([]Vector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.failWith != nil {
		return nil, c.failWith
	}

	c.calls++
	c.texts += len(texts)

	vectors := make([]Vector, len(texts))
	for index, text := range texts {
		// A deterministic, text-dependent vector so misalignment is detectable.
		vectors[index] = Vector{
			Space:  SpacePotion,
			Values: []float32{float32(len(text)), float32(text[0])},
		}
	}
	return vectors, nil
}

func (c *countingEmbedder) counts() (calls, texts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.texts
}

// The point of the cache: a health endpoint returning the same normalized body
// on every check should embed once, not once per check.
func TestCachingEmbedderSkipsRepeatedText(t *testing.T) {
	t.Parallel()

	inner := &countingEmbedder{}
	cache := NewCachingEmbedder(inner, 16)

	for range 50 {
		if _, err := cache.Embed(context.Background(), []string{`{"status":"ok"}`}); err != nil {
			t.Fatalf("Embed() error = %v", err)
		}
	}

	calls, texts := inner.counts()
	if calls != 1 || texts != 1 {
		t.Fatalf("inner embedder saw %d calls / %d texts, want 1 / 1", calls, texts)
	}

	hits, misses := cache.(*CachingEmbedder).Stats()
	if hits != 49 || misses != 1 {
		t.Fatalf("cache stats = %d hits / %d misses, want 49 / 1", hits, misses)
	}
}

// A mixed batch must delegate only the new texts and still return every vector
// in the caller's original order.
func TestCachingEmbedderPreservesOrderOnPartialHits(t *testing.T) {
	t.Parallel()

	inner := &countingEmbedder{}
	cache := NewCachingEmbedder(inner, 16)
	ctx := context.Background()

	if _, err := cache.Embed(ctx, []string{"beta"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	got, err := cache.Embed(ctx, []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	want, err := inner.Embed(ctx, []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	for index := range want {
		if Distance(got[index], want[index]) > 1e-6 {
			t.Fatalf("position %d has the wrong vector: %v, want %v",
				index, got[index].Values, want[index].Values)
		}
	}
}

func TestCachingEmbedderEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	inner := &countingEmbedder{}
	cache := NewCachingEmbedder(inner, 2)
	ctx := context.Background()

	for _, text := range []string{"one", "two", "three"} {
		if _, err := cache.Embed(ctx, []string{text}); err != nil {
			t.Fatalf("Embed() error = %v", err)
		}
	}

	// "one" was evicted when "three" arrived, so it must be recomputed.
	if _, err := cache.Embed(ctx, []string{"one"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	_, texts := inner.counts()
	if texts != 4 {
		t.Fatalf("inner embedder saw %d texts, want 4 (one two three one)", texts)
	}

	// "three" is still resident and must not be recomputed.
	if _, err := cache.Embed(ctx, []string{"three"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if _, texts = inner.counts(); texts != 4 {
		t.Fatalf("inner embedder saw %d texts after a hit, want 4", texts)
	}
}

func TestCachingEmbedderPropagatesErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("model unavailable")
	cache := NewCachingEmbedder(&countingEmbedder{failWith: sentinel}, 4)

	if _, err := cache.Embed(context.Background(), []string{"x"}); !errors.Is(err, sentinel) {
		t.Fatalf("Embed() error = %v, want %v", err, sentinel)
	}
}

func TestNewCachingEmbedderPassesThroughWhenDisabled(t *testing.T) {
	t.Parallel()

	inner := &countingEmbedder{}
	if got := NewCachingEmbedder(inner, 0); got != Embedder(inner) {
		t.Fatal("a zero capacity should return the inner embedder unwrapped")
	}
}

// Every probe goroutine embeds concurrently, so the cache must be race-free.
func TestCachingEmbedderIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	cache := NewCachingEmbedder(&countingEmbedder{}, 32)
	ctx := context.Background()

	var group sync.WaitGroup
	for worker := range 16 {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for round := range 40 {
				text := fmt.Sprintf("probe-%d-body-%d", worker%4, round%8)
				if _, err := cache.Embed(ctx, []string{text}); err != nil {
					t.Errorf("Embed() error = %v", err)
					return
				}
			}
		}(worker)
	}
	group.Wait()
}
