package seatnotice

import (
	"context"
	"sync"
)

// FakeMaxSubscriptions is how many subscriptions a [FakeHandler] keeps.
const FakeMaxSubscriptions = 4

// FakeHandler is a [Handler] that keeps its subscriptions in memory and
// records what it was asked, for the tests of the client and the seat and for
// the notifier's own. Its zero value is ready: it answers info with a fixed
// valid key, and every call with success unless Err is set.
type FakeHandler struct {
	mu sync.Mutex
	// VAPIDPublic is the key info answers with; a fixed valid key when empty.
	VAPIDPublic string
	// Err, when set, is returned by every call (a [*CodedError] is answered
	// as its code).
	Err error
	// Block, when set, is waited on by every call before it answers, or the
	// call's context ending (whichever is first, then Err of the context is
	// returned).
	Block <-chan struct{}

	subs   map[string]Subscription // by endpoint hash
	order  []string
	notify []int
	infos  int
}

// fakeKey is a valid 65-byte point's encoding (0x04 and 64 zero bytes), so a
// fake answers an info the client accepts.
const fakeKey = "BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (f *FakeHandler) wait(ctx context.Context) error {
	if f.Block != nil {
		select {
		case <-f.Block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Err
}

// Info answers with the key and the count of subscriptions kept.
func (f *FakeHandler) Info(ctx context.Context) (Info, error) {
	if err := f.wait(ctx); err != nil {
		return Info{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infos++
	k := f.VAPIDPublic
	if k == "" {
		k = fakeKey
	}
	return Info{VAPIDPublic: k, Subscriptions: len(f.subs)}, nil
}

// Notify records n and answers that it sent one push for each subscription.
func (f *FakeHandler) Notify(ctx context.Context, n int) (int, error) {
	if err := f.wait(ctx); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notify = append(f.notify, n)
	return len(f.subs), nil
}

// Subscribe keeps s, or replaces the subscription of its endpoint; it
// refuses a fifth endpoint with [CodeTooMany] and an invalid subscription
// with [CodeBadSubscription].
func (f *FakeHandler) Subscribe(ctx context.Context, s Subscription) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	if ValidateSubscription(s) != nil {
		return NewCodedError(CodeBadSubscription)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	h := EndpointSHA256(s.Endpoint)
	if _, ok := f.subs[h]; !ok {
		if len(f.subs) >= FakeMaxSubscriptions {
			return NewCodedError(CodeTooMany)
		}
		if f.subs == nil {
			f.subs = map[string]Subscription{}
		}
		f.order = append(f.order, h)
	}
	f.subs[h] = s
	return nil
}

// Unsubscribe drops the subscription of an endpoint hash, if there is one.
func (f *FakeHandler) Unsubscribe(ctx context.Context, endpointSHA256 string) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[endpointSHA256]; ok {
		delete(f.subs, endpointSHA256)
		for i, h := range f.order {
			if h == endpointSHA256 {
				f.order = append(f.order[:i], f.order[i+1:]...)
				break
			}
		}
	}
	return nil
}

// Notified returns the counts notify was called with, in order.
func (f *FakeHandler) Notified() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.notify...)
}

// Subscriptions returns the subscriptions kept, in the order they were made.
func (f *FakeHandler) Subscriptions() []Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Subscription, 0, len(f.order))
	for _, h := range f.order {
		out = append(out, f.subs[h])
	}
	return out
}

// Infos returns how many times info was answered.
func (f *FakeHandler) Infos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infos
}
