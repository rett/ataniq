//go:build linux && amd64

package qualification

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// HoldFrame is separate from Request/Observation. No streaming exception is
// added to the public one-request/one-observation API.
type HoldFrame struct {
	Version      int          `json:"version"`
	RunID        string       `json:"run_id"`
	RequestID    string       `json:"request_id"`
	Kind         string       `json:"kind"`
	PID          int          `json:"pid"`
	Start        uint64       `json:"start"`
	Executable   FileIdentity `json:"executable"`
	PIDNamespace FileIdentity `json:"pid_namespace"`
	Reason       string       `json:"reason,omitempty"`
}

func decodeHold(r io.Reader) (HoldFrame, error) {
	var f HoldFrame
	if err := decode(r, 8192, &f); err != nil {
		return f, err
	}
	if f.Version != 1 || !validHex(f.RunID) || !validHex(f.RequestID) || f.PID < 1 || f.Start == 0 || f.Executable.Inode == 0 || f.PIDNamespace.Inode == 0 || f.Kind != "ready" && f.Kind != "final" || f.Kind == "ready" && f.Reason != "" || f.Kind == "final" && f.Reason != "cancelled" && f.Reason != "self-lifetime" {
		return f, errors.New("invalid hold frame")
	}
	return f, nil
}

// Returns at newline, before EOF. The controller must bound the read deadline.
func ReadHoldReady(r io.Reader) (HoldFrame, error) {
	b, err := bufio.NewReaderSize(io.LimitReader(r, 8193), 8193).ReadBytes('\n')
	if err != nil || len(b) > 8192 {
		return HoldFrame{}, errors.New("incomplete readiness frame")
	}
	f, err := decodeHold(bytes.NewReader(b))
	if err != nil || f.Kind != "ready" {
		return HoldFrame{}, errors.New("invalid readiness")
	}
	return f, nil
}

func emitHold(w io.Writer, f HoldFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func readyHold(r Request, out io.Writer, p Process, wait func() string) error {
	if r.Validate() != nil || r.Case != CaseInventory || p.PID < 1 || p.Start == 0 {
		return errors.New("hold takes inventory identity only")
	}
	f := HoldFrame{Version: 1, RunID: r.RunID, RequestID: r.RequestID, Kind: "ready", PID: p.PID, Start: p.Start, Executable: p.Executable, PIDNamespace: p.Namespaces["pid"]}
	if err := emitHold(out, f); err != nil {
		return err
	}
	f.Kind = "final"
	f.Reason = wait()
	return emitHold(out, f)
}

func ReadyHold(r Request, out io.Writer) error {
	p, err := process(os.Getpid())
	if err != nil {
		return err
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(ch)
	return readyHold(r, out, p, func() string {
		t := time.NewTimer(1500 * time.Millisecond)
		defer t.Stop()
		select {
		case <-ch:
			return "cancelled"
		case <-t.C:
			return "self-lifetime"
		}
	})
}
