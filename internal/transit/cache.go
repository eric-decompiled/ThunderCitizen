package transit

import (
	"context"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Cache primitives
//
// Two generic types — CacheSlot[T] and CacheMap[K,V] — back every cached
// data product in the transit service. Strategy is the most basic thing
// that works: on miss, run the loader and store the result; on hit, return
// the stored value. No warmers, no background refresh loop.
//
// CacheSlot optionally takes a TTL. If TTL > 0, the value is considered
// expired after that duration since it was loaded, and the next Get
// re-runs the loader. Only the `live` slot in RepoCache sets a TTL —
// everything else caches forever (historical data is immutable once
// loaded, and current-day partial data gets naturally rotated when the
// date-range cache key changes at midnight).
//
// Both types implement double-checked locking on cold loads: reader
// grabs an RLock, on miss upgrades to Lock, re-checks, calls the loader,
// stores. Concurrent cold Gets coalesce onto a single loader invocation.
// ---------------------------------------------------------------------------

// CacheSlot holds a single lazily-loaded value of type T. If ttl > 0, the
// value expires ttl after it was loaded and the next Get re-runs load.
type CacheSlot[T any] struct {
	mu       sync.RWMutex
	value    T
	loaded   bool
	loadedAt time.Time
	ttl      time.Duration // 0 = never expires
	name     string
	load     func(context.Context) (T, error)
}

// NewCacheSlot creates a slot that caches forever after the first successful
// load. name is used for log messages only.
func NewCacheSlot[T any](name string, load func(context.Context) (T, error)) *CacheSlot[T] {
	return &CacheSlot[T]{name: name, load: load}
}

// NewCacheSlotTTL creates a slot whose value expires ttl after each load,
// triggering a re-load on the next Get. Used by the `live` slot in
// RepoCache to keep dashboard data fresh without a background warmer.
func NewCacheSlotTTL[T any](name string, ttl time.Duration, load func(context.Context) (T, error)) *CacheSlot[T] {
	return &CacheSlot[T]{name: name, ttl: ttl, load: load}
}

// fresh reports whether the currently-stored value is still valid under the
// slot's TTL. Must be called with the lock held.
func (s *CacheSlot[T]) fresh() bool {
	if !s.loaded {
		return false
	}
	if s.ttl == 0 {
		return true
	}
	return time.Since(s.loadedAt) < s.ttl
}

// Get returns the cached value, lazy-loading on miss or TTL expiry.
// Concurrent cold Gets coalesce onto a single loader invocation.
func (s *CacheSlot[T]) Get(ctx context.Context) (T, error) {
	s.mu.RLock()
	if s.fresh() {
		v := s.value
		s.mu.RUnlock()
		return v, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fresh() {
		return s.value, nil
	}
	v, err := s.load(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	s.value = v
	s.loaded = true
	s.loadedAt = time.Now()
	return v, nil
}

// Peek returns the cached value and a flag indicating whether it's loaded
// and fresh (i.e., within TTL if one is set). Does not trigger a load.
// Mostly useful in tests — handlers should use Get to get lazy-load
// semantics.
func (s *CacheSlot[T]) Peek() (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value, s.fresh()
}

// Refresh unconditionally runs the loader and stores the result. Kept for
// tests and for ad-hoc manual refreshes; no one calls it in the hot path
// since warmers were dropped.
func (s *CacheSlot[T]) Refresh(ctx context.Context) error {
	v, err := s.load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.value = v
	s.loaded = true
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return nil
}

// CacheMap is CacheSlot keyed by K. Same lazy-load semantics per key; no
// TTL, no eviction. Used for data products where different callers want
// different keys (date range, route ID, variant name). The loader takes
// the key so the same function can serve any number of entries.
type CacheMap[K comparable, V any] struct {
	mu      sync.RWMutex
	entries map[K]V
	loaded  map[K]bool
	name    string
	load    func(context.Context, K) (V, error)
}

// NewCacheMap creates an empty keyed cache.
func NewCacheMap[K comparable, V any](name string, load func(context.Context, K) (V, error)) *CacheMap[K, V] {
	return &CacheMap[K, V]{
		entries: make(map[K]V),
		loaded:  make(map[K]bool),
		name:    name,
		load:    load,
	}
}

// Get returns the cached value for key, lazy-loading on miss.
func (m *CacheMap[K, V]) Get(ctx context.Context, key K) (V, error) {
	m.mu.RLock()
	if m.loaded[key] {
		v := m.entries[key]
		m.mu.RUnlock()
		return v, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded[key] {
		return m.entries[key], nil
	}
	v, err := m.load(ctx, key)
	if err != nil {
		var zero V
		return zero, err
	}
	m.entries[key] = v
	m.loaded[key] = true
	return v, nil
}

// Peek returns the cached value for key without triggering a load.
func (m *CacheMap[K, V]) Peek(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded[key] {
		var zero V
		return zero, false
	}
	return m.entries[key], true
}

// Refresh unconditionally reloads the value for key. Other keys are left
// alone. Kept for tests and ad-hoc refreshes.
func (m *CacheMap[K, V]) Refresh(ctx context.Context, key K) error {
	v, err := m.load(ctx, key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.entries[key] = v
	m.loaded[key] = true
	m.mu.Unlock()
	return nil
}
