// Command ataniq-witness serves one durable checkpoint store on the authority host.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ataniq/internal/authorityhost/decisionguard"
	"ataniq/internal/authorityhost/noaicheck"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link"
)

const usage = `usage: ataniq-witness run|check|status|acknowledge|init|recover|complete-publication --config FILE
       ataniq-witness enroll-submit --config FILE --prepared FILE --receipt-out FILE [--expect-digest HEX]
       ataniq-witness slot-read --config FILE [--archive-out FILE]
       ataniq-witness slot-release --config FILE --asked WORDS
       ataniq-witness slot-retire --config FILE --asked WORDS`
const ownerUID = 0 // The owner administers this private socket locally as root.
const ownerTimeout = 5 * time.Second

type command struct {
	op, configPath, prepared, receiptOut, expectDigest string
	archiveOut, asked                                  string
}

func parseCommand(args []string) (command, bool) {
	var cmd command
	if len(args) == 0 {
		return cmd, false
	}
	cmd.op = args[0]
	options := map[string]*string{"--config": &cmd.configPath}
	switch cmd.op {
	case "run", "check", "init", "status", "acknowledge", "recover", "complete-publication":
		if len(args) != 3 || args[1] != "--config" {
			return cmd, false
		}
		cmd.configPath = args[2]
		return cmd, true
	case "enroll-submit":
		options["--prepared"], options["--receipt-out"], options["--expect-digest"] = &cmd.prepared, &cmd.receiptOut, &cmd.expectDigest
	case "slot-read":
		options["--archive-out"] = &cmd.archiveOut
	case "slot-release", "slot-retire":
		options["--asked"] = &cmd.asked
	default:
		return cmd, false
	}
	for args = args[1:]; len(args) > 0; args = args[2:] {
		dst := options[args[0]]
		if dst == nil || len(args) < 2 || args[1] == "" || strings.HasPrefix(args[1], "--") {
			return cmd, false
		}
		*dst = args[1]
		delete(options, args[0])
	}
	delete(options, "--expect-digest")
	delete(options, "--archive-out")
	return cmd, len(options) == 0 && (cmd.asked == "" || strings.TrimSpace(cmd.asked) != "")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, "/", os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, hostRoot string, args []string, out, diag io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, usage)
		return 0
	}
	cmd, ok := parseCommand(args)
	if !ok {
		fmt.Fprintln(diag, usage)
		return 2
	}
	if err := execute(ctx, hostRoot, cmd, out); err != nil {
		// OS and codec errors can contain paths or input. Only fixed diagnoses
		// cross this boundary.
		var storage durable.StorageError
		var refusal enrollmentRefusal
		switch {
		case errors.As(err, &refusal):
			fmt.Fprintln(diag, refusal)
		case errors.As(err, &storage):
			fmt.Fprintln(diag, storage)
		case errors.Is(err, errSocket):
			fmt.Fprintln(diag, errSocket)
		case errors.Is(err, os.ErrExist):
			fmt.Fprintln(diag, "witness store or socket already exists")
		case errors.Is(err, cp.ErrUnknownOutcome):
			fmt.Fprintln(diag, "witness outcome unknown; check signed status")
		case errors.Is(err, errEgress), errors.Is(err, errConfig), errors.Is(err, errFiles), errors.Is(err, errIdentity):
			fmt.Fprintln(diag, err)
		default:
			fmt.Fprintln(diag, "witness operation refused")
		}
		return 1
	}
	return 0
}

func execute(ctx context.Context, hostRoot string, cmd command, out io.Writer) (retErr error) {
	op := cmd.op
	c, store, err := loadConfig(cmd.configPath)
	if err != nil {
		return err
	}
	if op == "enroll-submit" {
		return enrollSubmit(ctx, c, store, cmd, out, ownerAdministration)
	}
	if op == "status" || op == "acknowledge" || strings.HasPrefix(op, "slot-") {
		return ownerCall(ctx, c, store, cmd, out)
	}
	server, err := serviceConfig(c, store)
	if err != nil {
		return err
	}
	if op == "check" || op == "run" || op == "recover" {
		if err := checkSocket(c.Socket); err != nil {
			return err
		}
	}
	guardDir := filepath.Join(hostRoot, guardDirectory)
	if op == "check" || op == "run" {
		var guardErr *decisionguard.Error
		if err := decisionguard.Check(guardDir); err != nil && !(errors.As(err, &guardErr) && guardErr.Code == decisionguard.CodeInhibited) {
			return errFiles
		}
		address, err := noaicheck.WitnessEgressAddress(hostRoot, op == "check")
		want, _ := noaicheck.WitnessHomeAddress(c.Home)
		if err != nil || address.IsValid() && address != want {
			return errEgress
		}
		if err := checkState(store.Directory); err != nil {
			return err
		}
		if op == "check" {
			_, err = fmt.Fprintln(out, "witness check ok")
			return err
		}
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
	if op == "init" || op == "complete-publication" {
		if err := w.Close(); err != nil {
			return err
		}
		message := "witness store initialized"
		if op == "complete-publication" {
			message = "witness publication completed"
		}
		_, err := fmt.Fprintln(out, message)
		return err
	}
	defer func() { retErr = errors.Join(retErr, w.Close()) }()
	recoveryOnly := op == "recover"
	if !recoveryOnly {
		if err := w.Grant(c.Slot, store.HomePin, true); err != nil {
			return err
		}
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.Socket, Net: "unix"})
	if err != nil {
		return errSocket
	}
	g := &guardedWitness{Witness: w, slot: c.Slot, dir: guardDir, recoveryOnly: recoveryOnly, turn: make(chan struct{}, 1)}
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

func ownerAdministration(c config, store durable.Config) (cp.RecoveryAdministration, error) {
	id, err := loadTLS(c.OwnerTLS, c.OwnerPin)
	if err != nil {
		return nil, err
	}
	remote := link.Remote{Network: "unix", Address: c.Socket, ServerPin: c.ServicePin, WitnessPin: store.Pin, Epoch: c.Identity.Epoch}
	return remote.Administration(id)
}

func ownerCall(ctx context.Context, c config, store durable.Config, cmd command, out io.Writer) error {
	admin, err := ownerAdministration(c, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, ownerTimeout)
	defer cancel()
	if strings.HasPrefix(cmd.op, "slot-") {
		return slotCommand(ctx, admin, c, store, cmd, out)
	}
	if cmd.op == "acknowledge" {
		if err := admin.AcknowledgeStorageError(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "witness storage acknowledged")
		return err
	}
	return printStatus(ctx, admin, store.Pin, c.Identity.Epoch, out)
}

func printStatus(ctx context.Context, admin cp.RecoveryAdministration, pin ed25519.PublicKey, epoch uint64, out io.Writer) error {
	var challenge cp.ID
	rand.Read(challenge[:])
	r, err := admin.Status(ctx, challenge)
	if err != nil {
		return err
	}
	st, err := cp.VerifyDirectoryStatus(pin, epoch, challenge, r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "epoch=%d sequence=%d digest=%s slots=%d receipts=%d fences=%d inhibition=%q\n", st.Epoch, st.Sequence, st.Digest, st.Slots, st.Receipts, st.Fences, st.Inhibition)
	if err == nil && st.Inhibition != cp.Ready && st.Inhibition != cp.ServiceInhibited {
		return cp.ErrUnavailable
	}
	return err
}
