package auth

import (
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewRate(3, time.Minute)
	r.now = func() time.Time { return now }
	for i := range 3 {
		if ok, _ := r.Allow("a"); !ok {
			t.Fatalf("event %d refused", i+1)
		}
	}
	ok, wait := r.Allow("a")
	if ok || wait != time.Minute {
		t.Fatalf("4th event: %v %v", ok, wait)
	}
	if ok, _ := r.Allow("b"); !ok {
		t.Fatal("other key refused")
	}
	now = now.Add(time.Minute)
	if ok, _ := r.Allow("a"); !ok {
		t.Fatal("refused in the next window")
	}
}

func TestRateBlocked(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewRate(2, time.Minute)
	r.now = func() time.Time { return now }
	if r.Blocked("a") != 0 {
		t.Fatal("blocked without events")
	}
	r.Allow("a")
	if r.Blocked("a") != 0 {
		t.Fatal("blocked below the limit")
	}
	r.Allow("a")
	now = now.Add(20 * time.Second)
	if wait := r.Blocked("a"); wait != 40*time.Second {
		t.Fatalf("wait %v", wait)
	}
	if wait := r.Blocked("a"); wait != 40*time.Second {
		t.Fatal("Blocked counted an event")
	}
	now = now.Add(40 * time.Second)
	if r.Blocked("a") != 0 {
		t.Fatal("blocked in the next window")
	}
}

func TestLimiterAddressOnly(t *testing.T) {
	l := NewLimiter()
	for range addrFailures {
		if l.BlockedAddr("1.2.3.4") > 0 {
			t.Fatal("blocked too early")
		}
		l.FailAddr("1.2.3.4")
	}
	if l.BlockedAddr("1.2.3.4") == 0 || l.Blocked("alice", "1.2.3.4") == 0 {
		t.Fatal("address not blocked")
	}
	if l.Blocked("alice", "5.6.7.8") > 0 {
		t.Fatal("a name was blocked by address-only failures")
	}
}

// AllowFirst and BlockedFirst report the first refusal of a key once per
// window, so a caller can log one line however many requests are refused.
func TestRateFirstRefusal(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewRate(2, time.Minute)
	r.now = func() time.Time { return now }
	for range 2 {
		if ok, _, first := r.AllowFirst("a"); !ok || first {
			t.Fatalf("allowed event: ok=%v first=%v", ok, first)
		}
	}
	if wait, first := r.BlockedFirst("b"); wait != 0 || first {
		t.Fatalf("unblocked key: %v %v", wait, first)
	}
	if ok, _, first := r.AllowFirst("a"); ok || !first {
		t.Fatalf("first refusal: ok=%v first=%v", ok, first)
	}
	for range 100 {
		if ok, _, first := r.AllowFirst("a"); ok || first {
			t.Fatal("a later refusal was reported as the first")
		}
		if wait, first := r.BlockedFirst("a"); wait == 0 || first {
			t.Fatal("a later Blocked refusal was reported as the first")
		}
	}
	// A new window reports again; BlockedFirst alone reports too.
	now = now.Add(time.Minute)
	r.Allow("a")
	r.Allow("a")
	if wait, first := r.BlockedFirst("a"); wait != time.Minute || !first {
		t.Fatalf("next window: %v %v", wait, first)
	}
	if _, _, first := r.AllowFirst("a"); first {
		t.Fatal("reported twice in one window")
	}
}

func TestLimiterFirstRefusal(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for range userFailures {
		l.Fail("alice", "10.0.0.1")
	}
	if wait, first := l.BlockedFirst("alice", "10.0.0.1"); wait != time.Minute || !first {
		t.Fatalf("first refusal: %v %v", wait, first)
	}
	for range 100 {
		if wait, first := l.BlockedFirst("alice", "10.0.0.2"); wait == 0 || first {
			t.Fatal("a later refusal was reported as the first")
		}
	}
	// The next block of the name is reported again.
	now = now.Add(time.Minute)
	l.Fail("alice", "10.0.0.1")
	if wait, first := l.BlockedFirst("alice", "10.0.0.1"); wait != 2*time.Minute || !first {
		t.Fatalf("next block: %v %v", wait, first)
	}

	for range addrFailures {
		l.FailAddr("10.9.9.9")
	}
	if wait, first := l.BlockedAddrFirst("10.9.9.9"); wait == 0 || !first {
		t.Fatalf("address block: %v %v", wait, first)
	}
	if _, first := l.BlockedFirst("bob", "10.9.9.9"); first {
		t.Fatal("the address block was reported twice")
	}
	if wait, first := l.BlockedAddrFirst("10.0.0.3"); wait != 0 || first {
		t.Fatalf("unblocked address: %v %v", wait, first)
	}
}
