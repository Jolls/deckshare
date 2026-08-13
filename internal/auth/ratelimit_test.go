package auth

import (
	"sync"
	"testing"
	"time"
)

func TestLimiter_AllowsUnderLimitDeniesOver(t *testing.T) {
	l := newLimiter(3, time.Minute)
	fake := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fake }

	for i := 0; i < 3; i++ {
		ok, _ := l.Allow("k")
		if !ok {
			t.Fatalf("call %d: want allow", i)
		}
	}
	ok, retryAfter := l.Allow("k")
	if ok {
		t.Fatal("4th call: want deny")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want positive", retryAfter)
	}
}

func TestLimiter_WindowReset(t *testing.T) {
	l := newLimiter(1, time.Minute)
	fake := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fake }

	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("first call: want allow")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("second call within window: want deny")
	}

	fake = fake.Add(time.Minute + time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("call after window reset: want allow")
	}
}

func TestLimiter_DistinctKeysIndependent(t *testing.T) {
	l := newLimiter(1, time.Minute)
	fake := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fake }

	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("key a: want allow")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("key b: want allow (independent of a)")
	}
}

func TestLimiter_Sweep(t *testing.T) {
	l := newLimiter(1, time.Minute)
	fake := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fake }

	l.Allow("expired")
	fake = fake.Add(2 * time.Minute)
	l.Allow("live")

	l.Sweep()

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen["expired"]; ok {
		t.Error("expired bucket should have been swept")
	}
	if _, ok := l.seen["live"]; !ok {
		t.Error("live bucket should still be present")
	}
	if len(l.seen) != 1 {
		t.Errorf("len(seen) = %d, want 1", len(l.seen))
	}
}

func TestLimiter_ConcurrentAllow(t *testing.T) {
	const limit = 100
	l := newLimiter(limit, time.Minute)

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := l.Allow("k")
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != limit {
		t.Errorf("allowed = %d, want %d", allowed, limit)
	}
}
