package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/enrollment"
)

func observeCommand(cmd command, c config, p pairing, ep *link.Endpoint, out io.Writer) (err error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	l, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return observe(ctx, cmd, c, p, ep, l, out, nil)
}

func observe(ctx context.Context, cmd command, c config, p pairing, ep *link.Endpoint, l net.Listener, out io.Writer, fault enrollment.Fault) (err error) {
	defer func() {
		_, e := fmt.Fprintln(out, "managed_starts=0 launches=0")
		err = errors.Join(ownerAction(err), e)
	}()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ep, l, io.Discard); cancel() }()
	defer func() { cancel(); err = errors.Join(err, <-done) }()
	_, b, err := bindingStatus(c, p.home.key.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	if b == nil {
		return errors.New("binding: observe requires a finalized enrollment")
	}
	signer, err := wire.NewSigner(b.Header, p.home.key)
	if err != nil {
		return err
	}
	return observeStore(ctx, cmd, c, *b, p.home.key, ep, signer, out, fault)
}

func ownerAction(err error) error {
	var e *enrollment.Error
	if !errors.As(err, &e) {
		return err
	}
	action := map[enrollment.Reason]string{
		enrollment.HomeBehind:     "preserve the restored home and compare the witness prefix with the owner archive",
		enrollment.HomeAhead:      "preserve both journals and check the witness restore against the owner archive",
		enrollment.Fork:           "preserve both branches and retire the disputed mirror",
		enrollment.Retired:        "initialize a fresh identity in a new root and enroll at the next epoch",
		enrollment.Unreachable:    "restore witness polling and retry the partition test",
		enrollment.Occupied:       "stop and fence the previous writer, then release its session on the owner channel",
		enrollment.Interrupted:    "preserve the staged intent and reconcile the advance on the owner channel",
		enrollment.ClaimUncertain: "reconcile the session claim on the owner channel before retrying",
	}[e.Reason]
	if action == "" {
		return err
	}
	return fmt.Errorf("%w\nowner action: %s", err, action)
}

// Reopen a stopped handle at most twice. Only the exact witnessed successor
// counts as the interrupted sample. A signed, unchanged predecessor permits
// trying that sample again. Any other head requires owner reconciliation.
func observeStore(ctx context.Context, cmd command, c config, b enrollment.Binding, home ed25519.PrivateKey, ep *link.Endpoint, signer assignment.Signer, out io.Writer, fault enrollment.Fault) (err error) {
	root := enrollment.FileRoot{Path: c.State}
	var store *enrollment.Store
	defer func() {
		if store != nil {
			err = errors.Join(err, store.Close())
		}
	}()
	var before, after assignment.Checkpoint
	remaining := cmd.events
	for attempt := 1; attempt <= 3; attempt++ {
		_, session, err := ed25519.GenerateKey(nil)
		if err != nil {
			return err
		}

		client, err := ep.Connect(ctx, c.Slot, home, session)
		if err != nil {
			clear(session)
		}
		if errors.Is(err, cp.ErrUnavailable) {
			return &enrollment.Error{Reason: enrollment.Unreachable, Err: err}
		}
		if err != nil {
			return err
		}
		store, err = enrollment.Start(ctx, root, b, client, session, fault)
		clear(session)
		if err != nil {
			return err
		}
		ledger, err := store.Ledger()
		if err != nil {
			return err
		}
		if attempt > 1 {
			switch ledger.Checkpoint() {
			case after:
				remaining--
			case before:
			default:
				return errors.New("restart found a different head; preserve the ledger for owner reconciliation")
			}
		}
		for remaining > 0 {
			e, err := mirrorEvent(ledger, b.Header, signer)
			if err != nil {
				return err
			}
			next, err := assignment.Apply(ledger, e)
			if err != nil {
				return err
			}
			before, after = ledger.Checkpoint(), next.Checkpoint()
			_, err = enrollment.AppendAndAdvance(ctx, store, e)
			if err != nil {
				if _, probe := store.Ledger(); probe == nil || attempt == 3 || ctx.Err() != nil {
					return err
				}
				if closeErr := store.Close(); closeErr != nil {
					return errors.Join(err, closeErr)
				}
				store = nil
				break
			}
			ledger = next
			remaining--
		}
		if remaining == 0 {
			return compareObservation(ctx, cmd.archive, root, b, store, client, out)
		}
	}
	return errors.New("observation restart limit reached")
}
