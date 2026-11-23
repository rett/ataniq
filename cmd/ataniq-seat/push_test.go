package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/seatapp"
	"ataniq/internal/seatnotice"
)

// pushAdapterRig serves the package's own notifier over a socket of this
// test, allowed for this user, and returns the command's client of it.
func pushAdapterRig(t *testing.T, fake *seatnotice.FakeHandler) pushNotifier {
	t.Helper()
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "n.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no Unix sockets: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- seatnotice.Serve(ctx, ln, fake, seatnotice.ServeConfig{AllowedUID: uint32(os.Getuid())})
	}()
	t.Cleanup(func() { cancel(); <-done })
	c, err := seatnotice.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	return pushNotifier{c}
}

func goodPushSub() seatapp.PushSubscription {
	p := make([]byte, 65)
	p[0] = 0x04
	return seatapp.PushSubscription{
		Endpoint: "https://push.example/send/abc",
		P256DH:   base64.RawURLEncoding.EncodeToString(p),
		Auth:     base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
	}
}

// TestPushNotifierAdaptsTheClient: the app's PushPort over the real client
// and server: info, subscribe and unsubscribe reach the notifier, its
// refusals come back as the app's PushError with the notifier's code, and
// notify is the client's own.
func TestPushNotifierAdaptsTheClient(t *testing.T) {
	fake := &seatnotice.FakeHandler{}
	n := pushAdapterRig(t, fake)
	ctx := context.Background()

	key, subs, err := n.Info(ctx)
	if err != nil || subs != 0 || !seatnotice.ValidVAPIDPublic(key) {
		t.Fatalf("info: %q %d %v", key, subs, err)
	}
	s := goodPushSub()
	if err := n.Subscribe(ctx, s); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	got := fake.Subscriptions()
	if len(got) != 1 || got[0].Endpoint != s.Endpoint || got[0].P256DH != s.P256DH || got[0].Auth != s.Auth {
		t.Errorf("the notifier kept %v", got)
	}
	if _, subs, _ := n.Info(ctx); subs != 1 {
		t.Errorf("info after: %d subscriptions", subs)
	}
	if err := n.Notify(ctx, 3); err != nil || len(fake.Notified()) != 1 || fake.Notified()[0] != 3 {
		t.Errorf("notify: %v %v", err, fake.Notified())
	}
	// A fifth endpoint is refused with the notifier's code.
	for i := 0; i < seatnotice.FakeMaxSubscriptions; i++ {
		x := s
		x.Endpoint = "https://push.example/send/" + strings.Repeat("x", i+1)
		err = n.Subscribe(ctx, x)
	}
	var pe *seatapp.PushError
	if !errors.As(err, &pe) || pe.Code != seatnotice.CodeTooMany {
		t.Errorf("a fifth subscription: %v", err)
	}
	if err := n.Unsubscribe(ctx, seatnotice.EndpointSHA256(s.Endpoint)); err != nil {
		t.Errorf("unsubscribe: %v", err)
	}
	if got := fake.Subscriptions(); len(got) != seatnotice.FakeMaxSubscriptions-1 {
		t.Errorf("after unsubscribe the notifier keeps %d", len(got))
	}
	// A notifier that is not there is no refusal.
	away, err := seatnotice.NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (pushNotifier{away}).Info(ctx); err == nil || errors.As(err, &pe) {
		t.Errorf("a notifier that is not there: %v", err)
	}
	// A failure of the notifier's own is the code it gives.
	fake.Err = errors.New("anything")
	if _, _, err := n.Info(ctx); !errors.As(err, &pe) || pe.Code != seatnotice.CodeUnavailable {
		t.Errorf("a failure: %v", err)
	}
	fake.Err = seatnotice.NewCodedError("endpoint_refused")
	if err := n.Subscribe(ctx, s); !errors.As(err, &pe) || pe.Code != "endpoint_refused" {
		t.Errorf("a coded failure: %v", err)
	}
}

// TestPushValidationMatchesTheNotifiers: the app's own check of a
// subscription's form (it may not import package seatnotice) gives the same
// answer as the notifier's for every case below, so a subscription the seat
// lets through is not one the notifier refuses for its form, and the other
// way round.
func TestPushValidationMatchesTheNotifiers(t *testing.T) {
	good := goodPushSub()
	p := make([]byte, 65)
	p[0] = 0x04
	bad05 := append([]byte{0x05}, p[1:]...)
	enc := base64.RawURLEncoding.EncodeToString
	with := func(f func(*seatapp.PushSubscription)) seatapp.PushSubscription {
		s := good
		f(&s)
		return s
	}
	atCap := "https://push.example/" + strings.Repeat("a", seatnotice.MaxEndpoint-len("https://push.example/"))
	cases := []seatapp.PushSubscription{
		good,
		with(func(s *seatapp.PushSubscription) { s.Endpoint = atCap }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = atCap + "a" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "http://push.example/x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "HTTPS://push.example/x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https:///x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://u:p@push.example/x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/x#f" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/x?a=b" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example:8443/x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/a b" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/a\x7fb" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/é" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://[::1]/x" }),
		with(func(s *seatapp.PushSubscription) { s.Endpoint = "https://push.example/%zz" }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = "" }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = enc(bad05) }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = enc(p[:64]) }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = enc(append(p, 0)) }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = good.P256DH + "=" }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = base64.StdEncoding.EncodeToString(p) }),
		with(func(s *seatapp.PushSubscription) { s.P256DH = good.P256DH[:len(good.P256DH)-1] + "!" }),
		with(func(s *seatapp.PushSubscription) { s.Auth = "" }),
		with(func(s *seatapp.PushSubscription) { s.Auth = enc(make([]byte, 15)) }),
		with(func(s *seatapp.PushSubscription) { s.Auth = enc(make([]byte, 17)) }),
		with(func(s *seatapp.PushSubscription) { s.Auth = "MDEyMzQ1Njc4OWFiY2RlZh" }),
		with(func(s *seatapp.PushSubscription) { s.Auth = good.Auth + "==" }),
	}
	for i, s := range cases {
		app := seatapp.ValidPushSubscription(s)
		notifier := seatnotice.ValidateSubscription(seatnotice.Subscription{Endpoint: s.Endpoint, P256DH: s.P256DH, Auth: s.Auth}) == nil
		if app != notifier {
			t.Errorf("case %d (%q): the app says %v, the notifier %v", i, s.Endpoint, app, notifier)
		}
	}
	if !seatapp.ValidPushSubscription(good) {
		t.Error("the good subscription is refused")
	}
}
