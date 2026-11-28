package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"ataniq/internal/checkpoint/link"
)

func listenAndServe(address string, ep *link.Endpoint, out io.Writer, listen func(string, string) (net.Listener, error)) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	l, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, ep, l, out)
}

func serve(ctx context.Context, ep *link.Endpoint, l net.Listener, out io.Writer) error {
	defer l.Close()
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	if _, err := fmt.Fprintln(out, "home serve started"); err != nil {
		return err
	}
	err := ep.Serve(l)
	if ctx.Err() != nil && errors.Is(err, net.ErrClosed) {
		err = nil
	}
	_, logErr := fmt.Fprintln(out, "home serve stopped")
	return errors.Join(err, logErr)
}
