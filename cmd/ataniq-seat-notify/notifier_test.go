package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/seatnotice"
	"ataniq/internal/webpush"
)

func code(t *testing.T, err error) string {
	t.Helper()
	var ce *seatnotice.CodedError
	if !errors.As(err, &ce) {
		t.Fatalf("not a coded error: %v", err)
	}
	return ce.Code
}

func TestNotifySendsTheCountToEverySubscription(t *testing.T) {
	p := newPush(t)
	n, _, lg, _ := testNotifier(t, p)
	ctx := context.Background()
	for _, path := range []string{"/p/one", "/p/two", "/p/three"} {
		if err := n.Subscribe(ctx, p.subscription(t, path)); err != nil {
			t.Fatal(err)
		}
	}
	sent, err := n.Notify(ctx, 7)
	if err != nil || sent != 3 {
		t.Fatalf("sent %d: %v", sent, err)
	}
	seen := p.requests()
	if len(seen) != 3 {
		t.Fatalf("%d pushes", len(seen))
	}
	for i, want := range []string{"/p/one", "/p/two", "/p/three"} {
		r := seen[i]
		if r.path != want {
			t.Errorf("push %d to %s, want %s (in the order they were made)", i, r.path, want)
		}
		if r.err != nil || string(r.payload) != `{"n":7}` {
			t.Errorf("payload %q: %v", r.payload, r.err)
		}
		for k, v := range map[string]string{
			"Topic": "waiting", "Urgency": "normal", "Ttl": "3600", "Content-Encoding": "aes128gcm",
		} {
			if got := r.header.Get(k); got != v {
				t.Errorf("%s: %q, want %q", k, got, v)
			}
		}
		if !strings.HasPrefix(r.header.Get("Authorization"), "vapid t=") || !strings.HasSuffix(r.header.Get("Authorization"), ", k="+p.key.PublicKeyString()) {
			t.Errorf("Authorization %q", r.header.Get("Authorization"))
		}
	}
	// Zero and the largest count are sent as they are.
	for _, c := range []int{0, 999} {
		if _, err := n.Notify(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	all := p.requests()
	if string(all[3].payload) != `{"n":0}` || string(all[6].payload) != `{"n":999}` {
		t.Errorf("payloads %q %q", all[3].payload, all[6].payload)
	}
	if _, err := n.Notify(ctx, 1000); err == nil {
		t.Error("a count over 999 was sent")
	}
	if _, err := n.Notify(ctx, -1); err == nil {
		t.Error("a negative count was sent")
	}
	// The log holds counts and fixed text, nothing of an endpoint or a key.
	text := lg.text()
	for _, bad := range []string{p.srv.URL, p.host(), "/p/one", p.key.PublicKeyString(), "vapid", "mailto"} {
		if bad != "" && strings.Contains(text, bad) {
			t.Errorf("the log holds %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "notify: 3 of 3 pushes accepted") {
		t.Errorf("the log:\n%s", text)
	}
}

func TestNotifyDropsGoneAndKeepsTheRest(t *testing.T) {
	p := newPush(t)
	n, st, _, _ := testNotifier(t, p)
	ctx := context.Background()
	subs := map[string]seatnotice.Subscription{}
	for _, path := range []string{"/p/a", "/p/b", "/p/c", "/p/d"} {
		subs[path] = p.subscription(t, path)
		if err := n.Subscribe(ctx, subs[path]); err != nil {
			t.Fatal(err)
		}
	}
	p.respond("/p/a", http.StatusGone)       // gone: dropped
	p.respond("/p/b", http.StatusNotFound)   // gone: dropped
	p.respond("/p/c", http.StatusBadGateway) // a failure: kept
	sent, err := n.Notify(ctx, 2)
	if err != nil || sent != 1 {
		t.Fatalf("sent %d: %v", sent, err)
	}
	left := st.list()
	if len(left) != 2 || left[0] != subs["/p/c"] || left[1] != subs["/p/d"] {
		t.Fatalf("kept %v", left)
	}
	if info, _ := n.Info(ctx); info.Subscriptions != 2 {
		t.Errorf("info says %d", info.Subscriptions)
	}
	// Every other failure keeps the subscription, and none sent is
	// unavailable so the seat tries again.
	p.respond("/p/d", http.StatusTooManyRequests)
	for _, c := range []int{http.StatusBadGateway, http.StatusRequestEntityTooLarge, http.StatusBadRequest, http.StatusForbidden} {
		p.respond("/p/c", c)
		if sent, err := n.Notify(ctx, 2); sent != 0 || code(t, err) != seatnotice.CodeUnavailable {
			t.Fatalf("status %d: sent %d: %v", c, sent, err)
		}
		if st.count() != 2 {
			t.Fatalf("status %d dropped a subscription", c)
		}
	}
	// A subscription that is gone, the last one left, is a success with none
	// sent: there is nothing more to try.
	p.respond("/p/c", http.StatusGone)
	p.respond("/p/d", http.StatusGone)
	if sent, err := n.Notify(ctx, 2); sent != 0 || err != nil {
		t.Fatalf("sent %d: %v", sent, err)
	}
	if st.count() != 0 {
		t.Errorf("%d kept", st.count())
	}
	// Nothing to send to is a success too.
	before := len(p.requests())
	if sent, err := n.Notify(ctx, 2); sent != 0 || err != nil || len(p.requests()) != before {
		t.Fatalf("with no subscription: sent %d: %v", sent, err)
	}
}

func TestSubscribeRules(t *testing.T) {
	p := newPush(t)
	n, st, _, _ := testNotifier(t, p)
	ctx := context.Background()
	good := p.subscription(t, "/p/1")

	// An endpoint on a host the policy does not allow.
	other := good
	other.Endpoint = "https://push.example/p/1"
	if c := code(t, n.Subscribe(ctx, other)); c != seatnotice.CodeEndpointRefused {
		t.Errorf("host not allowed: %s", c)
	}
	// Not a valid subscription: not https, a short key, a point off the
	// curve.
	bad := good
	bad.Endpoint = "http://" + p.host() + "/p/1"
	off := good
	off.P256DH = b64(append([]byte{0x04}, make([]byte, 64)...))
	short := good
	short.Auth = "AAAA"
	for name, s := range map[string]seatnotice.Subscription{"http": bad, "off the curve": off, "short auth": short} {
		if c := code(t, n.Subscribe(ctx, s)); c != seatnotice.CodeBadSubscription {
			t.Errorf("%s: %s", name, c)
		}
	}
	if st.count() != 0 {
		t.Fatal("a refused subscription was kept")
	}

	// Four are kept, the fifth is too many, and the same endpoint again
	// replaces its keys even when the store is full.
	subs := []seatnotice.Subscription{good}
	if err := n.Subscribe(ctx, good); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/p/2", "/p/3", "/p/4"} {
		s := p.subscription(t, path)
		subs = append(subs, s)
		if err := n.Subscribe(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if c := code(t, n.Subscribe(ctx, p.subscription(t, "/p/5"))); c != seatnotice.CodeTooMany {
		t.Errorf("fifth: %s", c)
	}
	renewed := p.subscription(t, "/p/1")
	if err := n.Subscribe(ctx, renewed); err != nil {
		t.Fatal(err)
	}
	got := st.list()
	if len(got) != 4 || got[0] != renewed || got[1] != subs[1] {
		t.Errorf("after the replacement: %v", got)
	}
	if info, _ := n.Info(ctx); info.VAPIDPublic != p.key.PublicKeyString() || info.Subscriptions != 4 {
		t.Errorf("info %+v", info)
	}

	// Unsubscribe by the endpoint's hash; one that is not there is fine.
	if err := n.Unsubscribe(ctx, seatnotice.EndpointSHA256(subs[1].Endpoint)); err != nil {
		t.Fatal(err)
	}
	if err := n.Unsubscribe(ctx, seatnotice.EndpointSHA256("https://nothing.example/")); err != nil {
		t.Fatal(err)
	}
	if st.count() != 3 {
		t.Errorf("%d kept", st.count())
	}
	if c := code(t, n.Unsubscribe(ctx, "nothex")); c != seatnotice.CodeBadSubscription {
		t.Errorf("a bad hash: %s", c)
	}
}

func TestEndpointPolicy(t *testing.T) {
	allow := endpointAllowed(webpush.AllowHosts("web.push.apple.com"))
	for endpoint, want := range map[string]bool{
		"https://web.push.apple.com/send/abc":       true,
		"https://WEB.push.apple.com/send/abc":       true,
		"https://web.push.apple.com:443/send/abc":   true,
		"https://web.push.apple.com:8443/send/abc":  false,
		"http://web.push.apple.com/send/abc":        false,
		"https://api.push.apple.com/send/abc":       false,
		"https://x.web.push.apple.com/send/abc":     false,
		"https://web.push.apple.com.evil.example/x": false,
		"https://user@web.push.apple.com/send/abc":  false,
		"https://fcm.googleapis.com/fcm/send/abc":   false,
		"https://127.0.0.1/send/abc":                false,
		"https:///send/abc":                         false,
		"":                                          false,
	} {
		if got := allow(endpoint); got != want {
			t.Errorf("%q: %v, want %v", endpoint, got, want)
		}
	}
}

// TestNotifyBoundsTheTime: a push service that does not answer cannot hold
// the call past its context, and a call that finds another one sending waits
// for its turn only as long as its context allows.
func TestNotifyBoundsTheTime(t *testing.T) {
	p := newPush(t)
	n, st, _, _ := testNotifier(t, p)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold["/p/slow"] = hold
	p.mu.Unlock()
	if err := n.Subscribe(context.Background(), p.subscription(t, "/p/slow")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	sent, err := n.Notify(ctx, 1)
	if sent != 0 || code(t, err) != seatnotice.CodeUnavailable || time.Since(start) > 5*time.Second {
		t.Fatalf("sent %d after %v: %v", sent, time.Since(start), err)
	}
	if st.count() != 1 {
		t.Error("a slow push dropped the subscription")
	}
	close(hold)

	// A call whose turn is taken.
	n.turn <- struct{}{}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if sent, err := n.Notify(ctx2, 1); sent != 0 || code(t, err) != seatnotice.CodeUnavailable {
		t.Fatalf("with the turn taken: sent %d: %v", sent, err)
	}
	<-n.turn
}

// TestNotifyOneAtATime: concurrent calls send in turn, not at once.
func TestNotifyOneAtATime(t *testing.T) {
	p := newPush(t)
	n, _, _, _ := testNotifier(t, p)
	if err := n.Subscribe(context.Background(), p.subscription(t, "/p/1")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := n.Notify(context.Background(), 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := len(p.requests()); got != 8 {
		t.Errorf("%d pushes, want 8", got)
	}
}
