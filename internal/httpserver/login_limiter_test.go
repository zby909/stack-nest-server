package httpserver

import (
	"strconv"
	"testing"
	"time"
)

// TestLoginLimiterWindows 验证计数有界、满载时保留已登录流程的窗口、过期按顺序释放。
func TestLoginLimiterWindows(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	if !l.allow("first", 1) || l.allow("first", 1) {
		t.Fatal("first key must have one request per window")
	}
	now = now.Add(time.Second)
	for i := 1; i < maxLoginCounters; i++ {
		if !l.allow(strconv.Itoa(i), 2) {
			t.Fatalf("capacity exhausted at %d", i)
		}
	}
	if l.allow("new", 1) || !l.allow("1", 2) || l.allow("1", 2) {
		t.Fatal("full limiter must preserve existing counters and reject new keys")
	}
	now = now.Add(59 * time.Second)
	if !l.allow("new", 1) || l.allow("1", 2) || l.allow("another", 1) {
		t.Fatal("only the oldest slot should have expired")
	}
	now = now.Add(time.Minute)
	if !l.allow("1", 2) || len(l.counters) != 1 || len(l.order) != 1 {
		t.Fatal("expired clients must be removed before opening the next window")
	}
}
