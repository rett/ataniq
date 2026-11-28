//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadregistry"
)

// The exit codes of the canary command, beside the codes of [RunStatus] it
// shares: 0 done, 1 anything else, 2 usage, 20 no daemon, 21 unauthorized, 22 a
// protocol error.
const (
	// ExitRefused is the daemon answering the error "refused".
	ExitRefused = 30
	// ExitUnavailable is the daemon answering the error "unavailable".
	ExitUnavailable = 31
)

// Timeouts of the canary command, dial included. The inject wait is longer than
// the daemon's whole exchange with the holder, which is bounded by
// [DefaultDialTimeout] for the dial and the write together and
// [DefaultInjectWait] for the answer (25 s), so the daemon's answer, uncertain
// included, arrives first.
const (
	// CanaryInjectTimeout bounds a canary inject call.
	CanaryInjectTimeout = 30 * time.Second
	// CanaryCallTimeout bounds every other canary call.
	CanaryCallTimeout = 60 * time.Second
)

// CanaryCall is one call of the canary command.
type CanaryCall struct {
	// Socket is the daemon's socket path. Required.
	Socket string
	// Op is one of [CanaryOperations]. Required.
	Op string
	// Role is the config's role, as text. Required.
	Role string
	// AttachRole is "writer" or "viewer", for ticket.
	AttachRole string
	// MessageID and Body are the message, for inject.
	MessageID, Body string
	// Timeout bounds the call; the default is [CanaryInjectTimeout] for inject
	// and [CanaryCallTimeout] for the others.
	Timeout time.Duration
}

// RunCanary calls one canary operation of the daemon at c.Socket, prints the
// result as one JSON line to stdout and returns the exit code: [ExitOK] when
// the daemon answered a result, [ExitRefused] and [ExitUnavailable] for the
// daemon's two errors, [ExitNoDaemon] when nothing answers at the socket,
// [ExitUnauthorized], [ExitProtocol] for anything else the daemon answered
// that is not a result and [ExitFailure] for the rest (a timeout, for one).
// Messages go to stderr, one line each; everything the daemon sent reaches the
// output escaped (see [SafeText]).
func RunCanary(ctx context.Context, c CanaryCall, stdout, stderr io.Writer) int {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CanaryCallTimeout
		if c.Op == OpCanaryInject {
			timeout = CanaryInjectTimeout
		}
	}
	var params any
	var result any
	switch c.Op {
	case OpCanaryDefine:
		params, result = CanaryParams{Role: c.Role}, new(CanaryDefineResult)
	case OpCanaryStart:
		params, result = CanaryParams{Role: c.Role}, new(CanaryStartResult)
	case OpCanaryStop:
		params, result = CanaryParams{Role: c.Role}, new(CanaryStopResult)
	case OpCanaryTicket:
		params, result = CanaryTicketParams{Role: c.Role, AttachRole: c.AttachRole}, new(CanaryTicketResult)
	case OpCanaryInject:
		params, result = CanaryInjectParams{Role: c.Role, MessageID: c.MessageID, Body: c.Body}, new(CanaryInjectResult)
	default:
		fmt.Fprintf(stderr, "ataniqd: %s is not a canary operation\n", SafeText(c.Op))
		return ExitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cl, err := clientapi.Dial(ctx, c.Socket)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTDIR):
			fmt.Fprintf(stderr, "ataniqd: no daemon answers at %s\n", SafeText(c.Socket))
			return ExitNoDaemon
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintf(stderr, "ataniqd: no answer from %s in %s\n", SafeText(c.Socket), timeout)
			return ExitFailure
		}
		fmt.Fprintf(stderr, "ataniqd: cannot connect to %s: %s\n", SafeText(c.Socket), SafeText(err.Error()))
		return ExitFailure
	}
	defer cl.Close()
	if err := cl.Call(ctx, c.Op, params, result); err != nil {
		return canaryFailure(c.Op, err, stderr)
	}
	line, err := asciiLine(result)
	if err != nil {
		fmt.Fprintf(stderr, "ataniqd: cannot encode the result: %s\n", SafeText(err.Error()))
		return ExitFailure
	}
	stdout.Write(line)
	return ExitOK
}

// canaryFailure prints the failure of a call and returns its exit code.
func canaryFailure(op string, err error, stderr io.Writer) int {
	var ce *clientapi.Error
	switch {
	case errors.As(err, &ce):
		switch ce.Code {
		case clientapi.CodeUnauthorized:
			fmt.Fprintln(stderr, "ataniqd: the daemon refused this user (unauthorized)")
			return ExitUnauthorized
		case CodeRefused, clientapi.ErrorCode(leadregistry.ErrHomeMissing), clientapi.ErrorCode(leadregistry.ErrOtherHome),
			clientapi.ErrorCode(leadregistry.ErrUnassigned), clientapi.ErrorCode(leadregistry.ErrAssignmentBusy),
			clientapi.ErrorCode(leadregistry.ErrStaleGeneration), clientapi.ErrorCode(leadregistry.ErrStaleRevision):
			fmt.Fprintf(stderr, "ataniqd: %s refused: %s\n", op, SafeText(ce.Message))
			return ExitRefused
		case CodeUnavailable:
			fmt.Fprintf(stderr, "ataniqd: %s unavailable: %s\n", op, SafeText(ce.Message))
			return ExitUnavailable
		}
		fmt.Fprintf(stderr, "ataniqd: the daemon answered an error to %s: %s: %s\n", op, SafeText(string(ce.Code)), SafeText(ce.Message))
		return ExitProtocol
	case errors.Is(err, clientapi.ErrBadResponse), errors.Is(err, clientapi.ErrResponseTooLarge),
		errors.Is(err, clientapi.ErrBadParams), errors.Is(err, clientapi.ErrRequestTooLarge):
		fmt.Fprintf(stderr, "ataniqd: protocol error: %s\n", SafeText(err.Error()))
		return ExitProtocol
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(stderr, "ataniqd: the daemon did not answer %s in time\n", op)
		return ExitFailure
	}
	fmt.Fprintf(stderr, "ataniqd: %s failed: %s\n", op, SafeText(err.Error()))
	return ExitFailure
}

// asciiLine encodes v as one line of JSON, in ASCII, ended by LF (see
// asciiJSON for what is escaped).
func asciiLine(v any) ([]byte, error) {
	b, err := asciiJSON(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
