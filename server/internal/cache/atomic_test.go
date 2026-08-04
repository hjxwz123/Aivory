package cache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryTakeIsSingleUseUnderConcurrency(t *testing.T) {
	c := NewMemory()
	c.Set("ticket", "payload", time.Minute)

	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if value, ok := c.Take("ticket"); ok && value == "payload" {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful Take calls = %d, want 1", got)
	}
}

func TestMemoryCompareAndDeleteAndSetNXAreAtomic(t *testing.T) {
	c := NewMemory()
	c.Set("code", "123456", time.Minute)
	if c.CompareAndDelete("code", "000000") {
		t.Fatal("CompareAndDelete accepted the wrong value")
	}
	if !c.CompareAndDelete("code", "123456") {
		t.Fatal("CompareAndDelete rejected the stored value")
	}
	if c.CompareAndDelete("code", "123456") {
		t.Fatal("CompareAndDelete consumed the same value twice")
	}

	if !c.SetNX("used", "1", time.Minute) {
		t.Fatal("first SetNX failed")
	}
	if c.SetNX("used", "2", time.Minute) {
		t.Fatal("second SetNX replaced an existing value")
	}
}
