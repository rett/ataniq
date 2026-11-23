package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLimiterWindow(t *testing.T) {
	ck := newClock()
	l := newLimiter(3, time.Hour, ck.now)
	for i := 0; i < 3; i++ {
		if !l.take() {
			t.Fatalf("take %d refused", i)
		}
		ck.advance(10 * time.Minute)
	}
	// 30 minutes in: three taken at 0, 10 and 20 minutes.
	if l.take() {
		t.Fatal("a fourth in the hour")
	}
	ck.advance(29 * time.Minute) // 59 minutes: the first is still inside
	if l.take() {
		t.Fatal("allowed before the first left the window")
	}
	ck.advance(time.Minute) // 60 minutes: the first is out, the others in
	if !l.take() {
		t.Fatal("refused after the first left the window")
	}
	if l.take() {
		t.Fatal("two places came free for one")
	}
	// A refused take spends nothing.
	ck.advance(10 * time.Minute) // the second (10 min) is out
	if !l.take() {
		t.Fatal("a refused take spent a place")
	}
}

// TestNotifyHourlyBound: at most 30 pushes in any hour, all subscriptions
// together; the one that would be the 31st is held back, and the seat is told
// rate_limited when none could be sent.
func TestNotifyHourlyBound(t *testing.T) {
	p := newPush(t)
	n, _, lg, ck := testNotifier(t, p)
	ctx := context.Background()
	for _, path := range []string{"/p/1", "/p/2"} {
		if err := n.Subscribe(ctx, p.subscription(t, path)); err != nil {
			t.Fatal(err)
		}
	}
	// 15 notifies of 2 pushes make 30.
	for i := 0; i < 15; i++ {
		if sent, err := n.Notify(ctx, 1); sent != 2 || err != nil {
			t.Fatalf("notify %d: sent %d: %v", i, sent, err)
		}
		ck.advance(time.Minute)
	}
	if got := len(p.requests()); got != 30 {
		t.Fatalf("%d pushes", got)
	}
	sent, err := n.Notify(ctx, 1)
	if sent != 0 || code(t, err) != codeRateLimited {
		t.Fatalf("the 31st: sent %d: %v", sent, err)
	}
	if got := len(p.requests()); got != 30 {
		t.Fatalf("a push went past the bound: %d", got)
	}
	// The pushes of the first notify (minute 0) leave the window at minute
	// 60: at minute 59 nothing is sent, at 60 two pushes are.
	ck.advance(44 * time.Minute) // 15 + 44 = 59 minutes
	if sent, err := n.Notify(ctx, 1); sent != 0 || code(t, err) != codeRateLimited {
		t.Fatalf("at 59 minutes: sent %d: %v", sent, err)
	}
	ck.advance(time.Minute)
	if sent, err := n.Notify(ctx, 1); sent != 2 || err != nil {
		t.Fatalf("at 60 minutes: sent %d: %v", sent, err)
	}
	// The next minute's two places came free the same way.
	ck.advance(time.Minute)
	if sent, err := n.Notify(ctx, 1); sent != 2 || err != nil {
		t.Fatalf("sent %d: %v", sent, err)
	}
	if sent, err := n.Notify(ctx, 1); sent != 0 || code(t, err) != codeRateLimited {
		t.Fatalf("sent %d: %v", sent, err)
	}
	if got := lg.text(); !strings.Contains(got, "2 held back by the hourly bound") {
		t.Errorf("the log does not say what was held back:\n%s", got)
	}
}

// TestNotifyPartialBound: when the bound allows some of the subscriptions,
// the call succeeds with those.
func TestNotifyPartialBound(t *testing.T) {
	p := newPush(t)
	n, _, lg, ck := testNotifier(t, p)
	n.limit = newLimiter(3, time.Hour, ck.now)
	ctx := context.Background()
	for _, path := range []string{"/p/1", "/p/2"} {
		if err := n.Subscribe(ctx, p.subscription(t, path)); err != nil {
			t.Fatal(err)
		}
	}
	if sent, err := n.Notify(ctx, 1); sent != 2 || err != nil {
		t.Fatalf("first: sent %d: %v", sent, err)
	}
	if sent, err := n.Notify(ctx, 1); sent != 1 || err != nil {
		t.Fatalf("second: sent %d: %v", sent, err)
	}
	if got := lg.text(); !strings.Contains(got, "1 held back by the hourly bound") {
		t.Errorf("the log:\n%s", got)
	}
	if _, err := n.Notify(ctx, 1); code(t, err) != codeRateLimited {
		t.Fatalf("third: %v", err)
	}
}
