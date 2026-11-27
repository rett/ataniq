//go:build ataniq_canary

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
)

const eventRow = `{"id":"tool","event":"Tool","fields":[{"name":"name","pointer":"/name"}],"digests":[{"name":"input","pointer":"/input"}],"output":"none","timeout_ms":5000,"async":true}`
const tableJSON = `{"schema":"ataniq.pack-declaration/1","builds":{"1":[` + eventRow + `]}}`
const input = `{"name":"ex\u001bample","input":{"value":"private"},"ignored":"omit"}`

func writeTable(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "table.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runReport(t *testing.T, start time.Time, args []string, in io.Reader, path string, dial dialFunc) error {
	t.Helper()
	stop := time.NewTimer(time.Hour)
	defer stop.Stop()
	return report(start, args, in, path, dial, stop)
}

// Unexpected connection operations (including a read on an event) fail the test.
type testConn struct {
	net.Conn
	written     bytes.Buffer
	writes      int
	closed      bool
	deadline    time.Time
	deadlineErr error
	writeErr    error
	short       bool
	reply       io.Reader
}

func (c *testConn) SetDeadline(d time.Time) error {
	c.deadline = d
	return c.deadlineErr
}

func (c *testConn) Write(p []byte) (int, error) {
	c.writes++
	if c.short || c.writeErr != nil {
		return 1, c.writeErr
	}
	return c.written.Write(p)
}

func (c *testConn) Read(p []byte) (int, error) { return c.reply.Read(p) }
func (c *testConn) Close() error {
	c.closed = true
	return nil
}

func invoke(t *testing.T, path, output string, in io.Reader, conn *testConn, dialErr error) error {
	t.Helper()
	start := time.Now()
	end := start.Add(300 * time.Millisecond)
	if output == "continuation" {
		end = start.Add(1500 * time.Millisecond)
	}
	calls := 0
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || deadline != end {
			t.Errorf("dial deadline %v, want %v", deadline, end)
		}
		if network != "unix" || address != "/ataniq/hookport/port" {
			t.Fatalf("unexpected destination %q %q", network, address)
		}
		return conn, dialErr
	}
	err := runReport(t, start, []string{"tool"}, in, path, dial)
	if calls != 1 || (dialErr == nil && (!conn.closed || conn.deadline != end)) {
		t.Fatalf("dial calls %d, closed %v, deadline %v", calls, conn.closed, conn.deadline)
	}
	return err
}

func TestReportOneObservation(t *testing.T) {
	for _, output := range []string{"none", "deny-from-table", "continuation"} {
		t.Run(output, func(t *testing.T) {
			path := writeTable(t, strings.Replace(tableJSON, `"none"`, fmt.Sprintf("%q", output), 1))
			conn := new(testConn)
			kind := hookwire.Event
			if output == "continuation" {
				kind = hookwire.Fetch
				none, err := hookwire.EncodeReply(hookwire.Reply{Version: 1, Kind: hookwire.None})
				if err != nil {
					t.Fatal(err)
				}
				conn.reply = iotest.OneByteReader(bytes.NewReader(none))
			}
			if err := invoke(t, path, output, strings.NewReader(input), conn, nil); err != nil {
				t.Fatal(err)
			}
			req, err := hookwire.DecodeRequest(conn.written.Bytes())
			want := hookwire.Request{
				Version: 1, Kind: kind, Row: "tool", Size: len(input),
				Fields:  map[string]string{"name": "example"},
				Digests: map[string]string{"input": fmt.Sprintf("%x", sha256.Sum256([]byte(`{"value":"private"}`)))},
			}
			if err != nil || !reflect.DeepEqual(req, &want) || conn.writes != 1 {
				t.Fatalf("frame = %+v, err %v, writes %d", req, err, conn.writes)
			}
		})
	}
}

func TestRefusedBeforeConnecting(t *testing.T) {
	path := writeTable(t, tableJSON)
	for _, tc := range []struct {
		name string
		args []string
		path string
		in   io.Reader
	}{
		{"no row", nil, path, nil},
		{"extra argument", []string{"tool", "extra"}, path, nil},
		{"undeclared row", []string{"other"}, path, nil},
		{"missing table", []string{"tool"}, filepath.Join(t.TempDir(), "missing"), nil},
		{"unreadable table", []string{"tool"}, t.TempDir(), nil},
		{"malformed table", []string{"tool"}, writeTable(t, tableJSON+"!"), nil},
		{"oversized table", []string{"tool"}, writeTable(t, strings.Repeat(" ", pack.MaxDeclarationBytes)+tableJSON), nil},
		{"two builds", []string{"tool"}, writeTable(t, strings.Replace(tableJSON, `"1":`, `"2":[],"1":`, 1)), nil},
		{"empty table", []string{"tool"}, writeTable(t, `{"schema":"ataniq.pack-declaration/1","builds":{}}`), nil},
		{"malformed input", []string{"tool"}, path, strings.NewReader(`{"name":`)},
		{"failed input", []string{"tool"}, path, iotest.ErrReader(io.ErrUnexpectedEOF)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runReport(t, time.Now(), tc.args, tc.in, tc.path, nil); err == nil {
				t.Fatal("accepted invalid observation")
			}
		})
	}
}

func TestInputLimitAndLookahead(t *testing.T) {
	path := writeTable(t, tableJSON)
	for _, extra := range []int{0, 10} {
		in := strings.NewReader(input + strings.Repeat(" ", hookwire.MaxInputBytes-len(input)+extra))
		conn := new(testConn)
		if err := invoke(t, path, "none", in, conn, nil); err != nil {
			t.Fatal(err)
		}
		req, err := hookwire.DecodeRequest(conn.written.Bytes())
		if err != nil || req.Size != hookwire.MaxInputBytes || req.Cut != (extra > 0) ||
			(len(req.Fields) == 0) != req.Cut || (len(req.Digests) == 0) != req.Cut || in.Len() != max(0, extra-1) {
			t.Fatalf("extra %d: frame %+v, err %v, unread %d", extra, req, err, in.Len())
		}
	}
}

func TestStalledInputForwardsOnlyCut(t *testing.T) {
	for _, output := range []string{"none", "continuation"} {
		t.Run(output, func(t *testing.T) {
			path := writeTable(t, strings.Replace(tableJSON, `"none"`, fmt.Sprintf("%q", output), 1))
			synctest.Test(t, func(t *testing.T) {
				reader, writer := io.Pipe()
				defer writer.Close()
				go func() { _, _ = io.WriteString(writer, input) }() // valid JSON, but no EOF
				conn := new(testConn)
				conn.reply = strings.NewReader("") // holder closes a fetch without an answer
				start := time.Now()
				err := invoke(t, path, output, reader, conn, nil)
				budget := 200 * time.Millisecond
				if output == "continuation" {
					budget = 250 * time.Millisecond
					if !errors.Is(err, io.EOF) {
						t.Fatalf("fetch close = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				req, err := hookwire.DecodeRequest(conn.written.Bytes())
				if err != nil || !req.Cut || req.Size != len(input) || len(req.Fields)+len(req.Digests) != 0 || time.Since(start) != budget {
					t.Fatalf("cut observation = %+v, %v", req, err)
				}
			})
		})
	}
}

func TestExpiredInputShareSkipsStdin(t *testing.T) {
	row, err := loadRow(writeTable(t, tableJSON), "tool")
	if err != nil {
		t.Fatal(err)
	}
	req, err := extract(row, nil, time.Now().Add(-time.Millisecond))
	if err != nil || !req.Cut || req.Size != 0 || len(req.Fields)+len(req.Digests) != 0 {
		t.Fatalf("expired input = %+v, %v", req, err)
	}
}

func TestTransportFailuresEndExchange(t *testing.T) {
	path := writeTable(t, tableJSON)
	for _, tc := range []struct {
		name   string
		conn   testConn
		dial   error
		want   error
		writes int
	}{
		{"port unavailable", testConn{}, io.EOF, io.EOF, 0},
		{"deadline unavailable", testConn{deadlineErr: io.ErrClosedPipe}, nil, io.ErrClosedPipe, 0},
		{"partial write", testConn{short: true}, nil, io.ErrShortWrite, 1},
		{"write timed out", testConn{writeErr: os.ErrDeadlineExceeded}, nil, os.ErrDeadlineExceeded, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := invoke(t, path, "none", strings.NewReader(input), &tc.conn, tc.dial)
			if !errors.Is(err, tc.want) || tc.conn.writes != tc.writes {
				t.Fatalf("err %v, writes %d", err, tc.conn.writes)
			}
		})
	}
}

func TestFetchReadsAtMostOneNone(t *testing.T) {
	frame := func(s string) []byte { return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...) }
	none := frame(`{"v":1,"k":"none"}`)
	for _, tc := range []struct {
		name  string
		data  []byte
		left  int
		valid bool
	}{
		{"one reply", append(bytes.Clone(none), none...), len(none), true},
		{"no header", nil, 0, false},
		{"short header", []byte{0, 1}, 0, false},
		{"short body", none[:len(none)-1], 0, false},
		{"decision", frame(`{"v":1,"k":"allow"}`), 0, false},
		{"oversized", frame(strings.Repeat("x", 33)), 33, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.data)
			if err := readNone(r); (err == nil) != tc.valid || r.Len() != tc.left {
				t.Fatalf("err %v, %d bytes left", err, r.Len())
			}
		})
	}
}

func TestMainExitsSilently(t *testing.T) {
	if len(os.Args) == 6 && os.Args[2] == "--" {
		for _, path := range os.Args[3:] {
			conn := new(testConn)
			body := []byte(`{"v":1,"k":"allow"}`)
			conn.reply = bytes.NewReader(append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...))
			run([]string{"tool"}, strings.NewReader(input), path, func(context.Context, string, string) (net.Conn, error) {
				return conn, nil
			})
			if !conn.closed || conn.writes != 1 {
				t.Fatal("incomplete exchange")
			}
		}
		os.Args = []string{"ataniq-hook"}
		main()
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitsSilently$", "--")
	for _, output := range []string{"none", "deny-from-table", "continuation"} {
		cmd.Args = append(cmd.Args, writeTable(t, strings.Replace(tableJSON, `"none"`, fmt.Sprintf("%q", output), 1)))
	}
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("output %q, err %v", output, err)
	}
}
