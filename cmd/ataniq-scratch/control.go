package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"ataniq/internal/leadholder/wire"
	"golang.org/x/sys/unix"
)

// callControl does one request on a holder's control socket, as the daemon
// would. The holder accepts a control peer only if it is the process whose pid
// is written in the daemon-lock file while that file is held with an exclusive
// flock (the lead-hosting design, section 2.1). So it opens the lock file
// (mode 0600), takes the exclusive flock (waiting up to lim.lockWait for
// another ataniq-scratch), writes its own pid (decimal, newline, truncating),
// connects, sends the request, reads the response and lets go of the lock.
//
// The error is for a conversation that did not happen (no lock, no socket, no
// answer, an answer that breaks the protocol). A refusal by the holder is a
// response with OK false, not an error.
func callControl(sock, lockPath string, req wire.Request, lim limits) (wire.Response, error) {
	payload, err := wire.EncodeRequest(req)
	if err != nil {
		return wire.Response{}, fmt.Errorf("request: %w", err)
	}
	lf, err := openPrivate(lockPath, os.O_RDWR, true)
	if err != nil {
		return wire.Response{}, fmt.Errorf("daemon lock: %w", err)
	}
	defer lf.Close()
	if err := flockWait(lf, unix.LOCK_EX, lim.lockWait); err != nil {
		return wire.Response{}, fmt.Errorf("daemon lock: %w", err)
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	if err := lf.Truncate(0); err != nil {
		return wire.Response{}, fmt.Errorf("daemon lock: %w", err)
	}
	if _, err := lf.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return wire.Response{}, fmt.Errorf("daemon lock: %w", err)
	}

	conn, err := (&net.Dialer{Timeout: lim.exchange}).Dial("unix", sock)
	if err != nil {
		return wire.Response{}, fmt.Errorf("control socket: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(lim.exchange)); err != nil {
		return wire.Response{}, err
	}
	if err := wire.NewWriter(conn, wire.ControlRequests).WriteFrame(wire.TypeRequest, payload); err != nil {
		return wire.Response{}, fmt.Errorf("send: %w", err)
	}
	fr, err := wire.NewReader(conn, wire.ControlResponses).Next()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return wire.Response{}, errors.New("the holder closed the connection without an answer")
		}
		return wire.Response{}, fmt.Errorf("receive: %w", err)
	}
	resp, err := wire.DecodeResponse(fr.Payload)
	if err != nil {
		return wire.Response{}, fmt.Errorf("answer: %w", err)
	}
	return resp, nil
}

// newRequest returns a request for op and epoch with a fresh command id.
func newRequest(op wire.Operation, epoch uint64) (wire.Request, error) {
	id, err := randomHex(8)
	if err != nil {
		return wire.Request{}, err
	}
	return wire.Request{Op: op, Epoch: epoch, CommandID: "scratch-" + id}, nil
}
