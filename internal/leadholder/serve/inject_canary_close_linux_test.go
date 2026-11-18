//go:build linux && ataniq_canary

package serve

import (
	"context"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// Closing the service while a deliverer that ignores its context runs: Close
// returns at once, the deliverer's context ends, the request in flight is
// answered ok with the closed result (never a refusal: the deliverer was
// called), and a request after the close is refused before the deliverer.
func TestInjectCloseWithStuckDeliverer(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	d := &fakeDeliverer{entered: make(chan struct{}, 2), fn: func(context.Context, string, string) wire.InjectResult {
		<-release // ignores the end of its context
		return submitted()
	}}
	e := canaryEnv(t, d)
	answer := make(chan wire.Response, 1)
	go func() { answer <- e.srv.inject(injectReq("inj-close")) }()
	<-d.entered
	closed := make(chan struct{})
	go func() { _ = e.srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(InjectTimeout / 3):
		t.Fatal("Close waits for the deliverer")
	}
	if _, _, ctx := d.first(); ctx.Err() == nil {
		t.Error("the deliverer's context did not end at Close")
	}
	want := injectUnknown("the holder closed during the delivery")
	select {
	case r := <-answer:
		if !r.OK || r.Inject == nil || *r.Inject != want {
			t.Errorf("the request in flight: %+v, want ok with %+v", r, want)
		}
	case <-time.After(InjectTimeout / 3):
		t.Fatal("no answer to the request in flight")
	}
	if r := e.srv.inject(injectReq("inj-late")); r.OK || r.Code != wire.CodeBusy {
		t.Errorf("after the close: %+v", r)
	}
	if d.calls() != 1 {
		t.Errorf("%d calls", d.calls())
	}
}
