// Command ataniq-witness serves one durable checkpoint store on the authority host.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link"
)

const usage = "usage: ataniq-witness run|check|status|acknowledge|init|recover|complete-publication --config FILE"
const ownerUID = 0 // The owner administers this private socket locally as root.

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out, diag io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, usage)
		return 0
	}
	if len(args) != 3 || args[1] != "--config" {
		fmt.Fprintln(diag, usage)
		return 2
	}
	switch args[0] {
	case "run", "check", "init", "status", "acknowledge", "recover", "complete-publication":
	default:
		fmt.Fprintln(diag, usage)
		return 2
	}
	if err := execute(ctx, args[0], args[2], out); err != nil {
		// OS and codec errors can contain paths or input. Only fixed diagnoses
		// cross this boundary.
		var storage durable.StorageError
		switch {
		case errors.As(err, &storage):
			fmt.Fprintln(diag, storage)
		case errors.Is(err, os.ErrExist):
			fmt.Fprintln(diag, "witness store or socket already exists")
		case errors.Is(err, cp.ErrUnknownOutcome):
			fmt.Fprintln(diag, "witness outcome unknown; check signed status")
		case errors.Is(err, errConfig), errors.Is(err, errFiles), errors.Is(err, errIdentity):
			fmt.Fprintln(diag, err)
		default:
			fmt.Fprintln(diag, "witness operation refused")
		}
		return 1
	}
	return 0
}

func execute(ctx context.Context, op, path string, out io.Writer) (retErr error) {
	c, store, err := loadConfig(path)
	if err != nil {
		return err
	}
	if op == "status" || op == "acknowledge" {
		return ownerCall(ctx, c, store, op, out)
	}
	server, err := serviceConfig(c, store)
	if err != nil {
		return err
	}
	if op == "check" {
		if err := checkState(store.Directory); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "witness check ok")
		return err
	}
	open := durable.Open
	switch op {
	case "init":
		open = durable.Create
	case "recover":
		open = durable.OpenForRecovery
	case "complete-publication":
		open = durable.CompletePublication
	}
	w, err := open(store)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, w.Close()) }()
	if op == "init" || op == "complete-publication" {
		return nil
	}
	recoveryOnly := op == "recover"
	if !recoveryOnly {
		if err := w.Grant(c.Slot, store.HomePin, true); err != nil {
			return err
		}
	}
	d, err := directory(filepath.Dir(c.Socket), true)
	if err != nil {
		return err
	}
	defer d.Close()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.Socket, Net: "unix"})
	if err != nil {
		return err
	}
	g := &guardedWitness{Witness: w, dir: c.Guard, recoveryOnly: recoveryOnly, turn: make(chan struct{}, 1)}
	return serve(ctx, server, g, ln, c.OwnerPin, ownerUID)
}

func serve(ctx context.Context, s *link.Server, g *guardedWitness, ln *net.UnixListener, pin string, uid uint32) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- s.ServeOwner(ln, ownerAdmin{g}, g, pin, uid) }()
	workers := 1
	if !g.recoveryOnly {
		workers++
		go func() { done <- s.Poll(ctx, g) }()
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-done:
		workers--
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = nil
	}
	cancel()
	ln.Close()
	for range workers {
		<-done
	}
	return err
}

func ownerCall(ctx context.Context, c config, store durable.Config, op string, out io.Writer) error {
	id, err := loadTLS(c.OwnerTLS, c.OwnerPin)
	if err != nil {
		return err
	}
	remote := link.Remote{Network: "unix", Address: c.Socket, ServerPin: c.ServicePin, WitnessPin: store.Pin, Epoch: c.Identity.Epoch}
	admin, err := remote.Administration(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if op == "acknowledge" {
		if err := admin.AcknowledgeStorageError(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "witness storage acknowledged")
		return err
	}
	var challenge cp.ID
	rand.Read(challenge[:])
	r, err := admin.Status(ctx, challenge)
	if err != nil {
		return err
	}
	st, err := cp.VerifyDirectoryStatus(store.Pin, c.Identity.Epoch, challenge, r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "epoch=%d sequence=%d digest=%s slots=%d receipts=%d fences=%d inhibition=%q\n", st.Epoch, st.Sequence, st.Digest, st.Slots, st.Receipts, st.Fences, st.Inhibition)
	if err == nil && st.Inhibition != cp.Ready && st.Inhibition != cp.ServiceInhibited {
		return cp.ErrUnavailable
	}
	return err
}
