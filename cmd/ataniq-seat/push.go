package main

import (
	"context"
	"errors"

	"ataniq/internal/seatapp"
	"ataniq/internal/seatnotice"
)

// pushNotifier is the notifier's client as the app takes it: its Notify is
// the client's own, and Info, Subscribe and Unsubscribe are the app's
// [seatapp.PushPort], which has no part of package seatnotice in its
// signatures (the app imports no other package of the module). A refusal of
// the notifier is handed on as a [*seatapp.PushError] with its code, and any
// other failure as it is.
type pushNotifier struct {
	*seatnotice.Client
}

var (
	_ seatapp.Notifier = pushNotifier{}
	_ seatapp.PushPort = pushNotifier{}
)

// pushError turns the client's refusal into the app's.
func pushError(err error) error {
	var ce *seatnotice.CodedError
	if errors.As(err, &ce) {
		return &seatapp.PushError{Code: ce.Code}
	}
	return err
}

// Notify is the client's, with a refusal handed on as a [*seatapp.PushError]:
// the app counts a call the notifier answered, with a code, toward its day's
// cap, and one that got no answer not.
func (n pushNotifier) Notify(ctx context.Context, count int) error {
	return pushError(n.Client.Notify(ctx, count))
}

func (n pushNotifier) Info(ctx context.Context) (string, int, error) {
	info, err := n.Client.Info(ctx)
	if err != nil {
		return "", 0, pushError(err)
	}
	return info.VAPIDPublic, info.Subscriptions, nil
}

func (n pushNotifier) Subscribe(ctx context.Context, s seatapp.PushSubscription) error {
	return pushError(n.Client.Subscribe(ctx, seatnotice.Subscription{Endpoint: s.Endpoint, P256DH: s.P256DH, Auth: s.Auth}))
}

func (n pushNotifier) Unsubscribe(ctx context.Context, endpointSHA256 string) error {
	return pushError(n.Client.Unsubscribe(ctx, endpointSHA256))
}
