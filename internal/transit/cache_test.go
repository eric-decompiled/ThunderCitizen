package transit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheSlot_GetLoadsOnFirstCall(t *testing.T) {
	var calls int32
	slot := NewCacheSlot("test", func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "loaded", nil
	})

	v, err := slot.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "loaded" {
		t.Fatalf("got %q, want loaded", v)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader called %d times, want 1", got)
	}
}

func TestCacheSlot_GetReusesCachedValue(t *testing.T) {
	var calls int32
	slot := NewCacheSlot("test", func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 42, nil
	})

	for i := 0; i < 10; i++ {
		v, err := slot.Get(context.Background())
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if v != 42 {
			t.Fatalf("got %d, want 42", v)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader called %d times, want 1 (cached)", got)
	}
}

func TestCacheSlot_ConcurrentGetCoalesces(t *testing.T) {
	// Many concurrent cold Gets should coalesce onto one loader invocation.
	var calls int32
	started := make(chan struct{})
	slot := NewCacheSlot("test", func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		<-started // block until test releases
		return 1, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot.Get(context.Background())
		}()
	}
	close(started)
	wg.Wait()

	// Note: with double-checked locking, multiple goroutines can race into
	// the write-lock section and re-check loaded before calling load. Exactly
	// one gets through. Verify loader was called exactly once.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader called %d times under concurrent load, want 1", got)
	}
}

func TestCacheSlot_PeekDoesNotLoad(t *testing.T) {
	var calls int32
	slot := NewCacheSlot("test", func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "x", nil
	})

	v, ok := slot.Peek()
	if ok {
		t.Fatalf("Peek on empty returned loaded=true, value=%q", v)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("Peek triggered loader %d times, want 0", got)
	}

	// After a Get, Peek should return the cached value.
	slot.Get(context.Background())
	v, ok = slot.Peek()
	if !ok || v != "x" {
		t.Fatalf("Peek after Get: ok=%v v=%q, want true, 'x'", ok, v)
	}
}

func TestCacheSlot_RefreshOverwrites(t *testing.T) {
	var value int32
	slot := NewCacheSlot("test", func(ctx context.Context) (int32, error) {
		return atomic.LoadInt32(&value), nil
	})

	atomic.StoreInt32(&value, 1)
	if v, _ := slot.Get(context.Background()); v != 1 {
		t.Fatalf("first Get: got %d, want 1", v)
	}

	atomic.StoreInt32(&value, 2)
	// Without Refresh, should still see cached value.
	if v, _ := slot.Get(context.Background()); v != 1 {
		t.Fatalf("second Get (no refresh): got %d, want 1 (cached)", v)
	}

	if err := slot.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if v, _ := slot.Get(context.Background()); v != 2 {
		t.Fatalf("after Refresh: got %d, want 2", v)
	}
}

func TestCacheSlot_TTLExpiryTriggersReload(t *testing.T) {
	var calls int32
	slot := NewCacheSlotTTL("ttl-test", 20*time.Millisecond, func(ctx context.Context) (int, error) {
		return int(atomic.AddInt32(&calls, 1)), nil
	})

	// First Get loads → 1.
	if v, _ := slot.Get(context.Background()); v != 1 {
		t.Fatalf("first Get: got %d, want 1", v)
	}
	// Second Get within TTL → still cached (1), loader not called again.
	if v, _ := slot.Get(context.Background()); v != 1 {
		t.Fatalf("second Get within TTL: got %d, want 1", v)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader called %d times within TTL, want 1", got)
	}

	// Wait past TTL. Next Get should re-load and return 2.
	time.Sleep(30 * time.Millisecond)
	if v, _ := slot.Get(context.Background()); v != 2 {
		t.Fatalf("Get after TTL: got %d, want 2 (reloaded)", v)
	}
}

func TestCacheSlot_TTLPeekReflectsExpiry(t *testing.T) {
	slot := NewCacheSlotTTL("ttl-peek", 20*time.Millisecond, func(ctx context.Context) (string, error) {
		return "hello", nil
	})

	slot.Get(context.Background())
	if _, ok := slot.Peek(); !ok {
		t.Fatal("Peek after Get: ok=false, want true")
	}

	time.Sleep(30 * time.Millisecond)
	if _, ok := slot.Peek(); ok {
		t.Fatal("Peek after TTL expiry: ok=true, want false")
	}
}

func TestCacheSlot_LoaderErrorDoesNotCache(t *testing.T) {
	var attempts int32
	boom := errors.New("boom")
	slot := NewCacheSlot("test", func(ctx context.Context) (string, error) {
		if n := atomic.AddInt32(&attempts, 1); n == 1 {
			return "", boom
		}
		return "ok", nil
	})

	if _, err := slot.Get(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("first Get: got err=%v, want boom", err)
	}
	if _, ok := slot.Peek(); ok {
		t.Fatalf("Peek after failed Get: loaded=true, want false")
	}
	// Next Get should retry the loader and succeed.
	if v, err := slot.Get(context.Background()); err != nil || v != "ok" {
		t.Fatalf("second Get: v=%q err=%v, want ok", v, err)
	}
}

func TestCacheMap_GetLazyLoadsPerKey(t *testing.T) {
	var calls int32
	seenKeys := make(chan string, 10)
	m := NewCacheMap("test", func(ctx context.Context, key string) (int, error) {
		atomic.AddInt32(&calls, 1)
		seenKeys <- key
		return len(key), nil
	})

	if v, _ := m.Get(context.Background(), "a"); v != 1 {
		t.Fatalf("Get a: got %d, want 1", v)
	}
	if v, _ := m.Get(context.Background(), "bb"); v != 2 {
		t.Fatalf("Get bb: got %d, want 2", v)
	}
	// Re-getting "a" should be cached.
	if v, _ := m.Get(context.Background(), "a"); v != 1 {
		t.Fatalf("re-Get a: got %d, want 1", v)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("loader called %d times, want 2 (once per distinct key)", got)
	}
}

func TestCacheMap_RefreshOverwritesSingleKey(t *testing.T) {
	values := map[string]int{"x": 10, "y": 20}
	m := NewCacheMap("test", func(ctx context.Context, key string) (int, error) {
		return values[key], nil
	})

	m.Get(context.Background(), "x")
	m.Get(context.Background(), "y")

	values["x"] = 999
	if err := m.Refresh(context.Background(), "x"); err != nil {
		t.Fatalf("Refresh x: %v", err)
	}

	if v, _ := m.Get(context.Background(), "x"); v != 999 {
		t.Fatalf("after Refresh x: got %d, want 999", v)
	}
	// y should be untouched.
	if v, _ := m.Get(context.Background(), "y"); v != 20 {
		t.Fatalf("y after Refresh x: got %d, want 20 (untouched)", v)
	}
}

func TestCacheMap_PeekNoLoad(t *testing.T) {
	var calls int32
	m := NewCacheMap("test", func(ctx context.Context, key string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "loaded", nil
	})

	if _, ok := m.Peek("absent"); ok {
		t.Fatalf("Peek on empty key: ok=true, want false")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("Peek triggered loader %d times, want 0", got)
	}
}
