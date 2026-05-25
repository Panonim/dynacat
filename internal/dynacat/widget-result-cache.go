package dynacat

import (
	"context"
	"sync"
	"time"
)

// widgetResultCache shares identical widget API results across widget instances
// while using each widget's existing cache policy as the result TTL.

const widgetResultErrorCacheDuration = time.Second

type widgetResultCache[T any] struct {
	mu         sync.Mutex
	generation uint64
	items      map[string]*widgetResultCacheEntry[T]
}

type widgetResultCacheEntry[T any] struct {
	value      T
	err        error
	expires    time.Time
	ready      chan struct{}
	generation uint64
}

func newWidgetResultCache[T any]() *widgetResultCache[T] {
	return &widgetResultCache[T]{items: make(map[string]*widgetResultCacheEntry[T])}
}

func (cache *widgetResultCache[T]) GetForWidget(ctx context.Context, widget *widgetBase, key string, fetch func(context.Context) (T, error)) (T, error) {
	return cache.get(ctx, key, widget.resultCacheDuration(), widgetResultErrorCacheDuration, fetch)
}

func (cache *widgetResultCache[T]) Clear() {
	cache.mu.Lock()
	cache.generation++
	cache.items = make(map[string]*widgetResultCacheEntry[T])
	cache.mu.Unlock()
}

func (cache *widgetResultCache[T]) get(ctx context.Context, key string, ttl time.Duration, errorTTL time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	for {
		now := time.Now()

		cache.mu.Lock()
		entry := cache.items[key]
		if entry != nil {
			if entry.ready == nil && now.Before(entry.expires) {
				value, err := entry.value, entry.err
				cache.mu.Unlock()
				return value, err
			}

			if entry.ready != nil {
				ready := entry.ready
				cache.mu.Unlock()

				select {
				case <-ready:
				case <-ctx.Done():
					var zero T
					return zero, ctx.Err()
				}

				cache.mu.Lock()
				if entry.generation != cache.generation {
					cache.mu.Unlock()
					continue
				}
				value, err := entry.value, entry.err
				cache.mu.Unlock()
				return value, err
			}
		}

		entry = &widgetResultCacheEntry[T]{ready: make(chan struct{}), generation: cache.generation}
		cache.items[key] = entry
		cache.mu.Unlock()

		value, err := fetch(ctx)

		cache.mu.Lock()
		if entry.generation != cache.generation {
			close(entry.ready)
			entry.ready = nil
			cache.mu.Unlock()
			continue
		}

		entry.value = value
		entry.err = err
		if err != nil {
			entry.expires = time.Now().Add(errorTTL)
		} else {
			entry.expires = time.Now().Add(ttl)
		}
		close(entry.ready)
		entry.ready = nil
		cache.mu.Unlock()

		return value, err
	}
}

func (w *widgetBase) resultCacheDuration() time.Duration {
	duration := time.Until(w.getNextUpdateTime())
	if duration < 0 {
		return 0
	}

	return duration
}
