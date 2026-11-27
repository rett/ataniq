//go:build ataniq_canary

// Command ataniq-hook reports one sandbox hook observation.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
)

const (
	tablePath = "/ataniq/hook/table.json"
	portPath  = "/ataniq/hookport/port"

	eventBudget = 300 * time.Millisecond
	eventInput  = 200 * time.Millisecond
	fetchBudget = 1500 * time.Millisecond
	fetchInput  = 250 * time.Millisecond
)

func main() {
	run(os.Args[1:], os.Stdin, tablePath, new(net.Dialer).DialContext)
}

type dialFunc func(context.Context, string, string) (net.Conn, error)

func run(args []string, stdin io.Reader, path string, dial dialFunc) {
	// Even a stalled table read or a busy decoder must let the provider resume.
	// All observation failures have the same provider response: silence, exit 0.
	start := time.Now()
	stop := time.AfterFunc(time.Until(start.Add(eventBudget)), func() { os.Exit(0) })
	defer stop.Stop()
	_ = report(start, args, stdin, path, dial, stop)
}

func report(start time.Time, args []string, stdin io.Reader, path string, dial dialFunc, stop *time.Timer) error {
	if len(args) != 1 {
		return errors.New("expected a hook row")
	}
	row, err := loadRow(path, args[0])
	if err != nil {
		return err
	}
	inputEnd, end := start.Add(eventInput), start.Add(eventBudget)
	if row.Output == "continuation" {
		inputEnd, end = start.Add(fetchInput), start.Add(fetchBudget)
		stop.Reset(time.Until(end))
	}

	req, err := extract(row, stdin, inputEnd)
	if err != nil {
		return err
	}
	frame, err := hookwire.EncodeRequest(*req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	conn, err := dial(ctx, "unix", portPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(end); err != nil {
		return err
	}
	n, err := conn.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	if req.Kind == hookwire.Fetch {
		return readNone(conn)
	}
	return nil
}

// The renderer selects the exact build. The provider supplies only a row ID.
func loadRow(path, id string) (pack.HookRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return pack.HookRow{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, pack.MaxDeclarationBytes+1))
	if err != nil {
		return pack.HookRow{}, err
	}
	builds, err := hookwire.DecodeTable(data)
	if err != nil {
		return pack.HookRow{}, err
	}
	if len(builds) != 1 {
		return pack.HookRow{}, errors.New("expected a single build in the hook table")
	}
	for _, table := range builds {
		if row, ok := table.Lookup(id); ok {
			return row, nil
		}
	}
	return pack.HookRow{}, errors.New("unknown hook row")
}

func extract(row pack.HookRow, stdin io.Reader, end time.Time) (*hookwire.Request, error) {
	// Inherited blocking stdin pipes need not support SetReadDeadline. Interrupt
	// the pipe into Extract even if the provider leaves stdin open mid-value.
	// A pending stdin read ends with this one-shot process.
	r, w := io.Pipe()
	defer r.Close()
	if !time.Now().Before(end) {
		w.CloseWithError(os.ErrDeadlineExceeded)
		return hookwire.Extract(row, r)
	}
	timer := time.AfterFunc(time.Until(end), func() { w.CloseWithError(os.ErrDeadlineExceeded) })
	defer timer.Stop()
	go func() {
		_, err := io.Copy(w, io.LimitReader(stdin, hookwire.MaxInputBytes+1))
		w.CloseWithError(err)
	}()
	return hookwire.Extract(row, r)
}

// In S1 only a none frame is expected. Consume one bounded reply without
// waiting for EOF. No reply content goes to the provider.
func readNone(r io.Reader) error {
	var frame [4 + hookwire.MaxNoneBytes]byte
	if _, err := io.ReadFull(r, frame[:4]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(frame[:4])
	if size > hookwire.MaxNoneBytes {
		return errors.New("oversized none reply")
	}
	if _, err := io.ReadFull(r, frame[4:4+size]); err != nil {
		return err
	}
	reply, err := hookwire.DecodeReply(frame[:4+size])
	if err == nil && reply.Kind != hookwire.None {
		return errors.New("unexpected hook reply")
	}
	return err
}
