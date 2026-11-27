package hookport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/agentsession/sessiontest"
	"ataniq/internal/pack"
	packhook "ataniq/packs/codex/hook"
	"golang.org/x/sys/unix"
)

type fakePeers struct {
	group, info, workerGroup, status string
	threads                          []string
	uid                              uint32
	alive, open                      bool
	t                                *testing.T
	trace                            []string
}

func peers(t *testing.T) *fakePeers {
	return &fakePeers{threads: []string{"123", "124"}, status: "State:\tS (sleeping)\n", workerGroup: "0::/unit\n", group: "0::/unit/agent\n", info: "Pid:\t123\n", uid: uint32(os.Geteuid()), alive: true, t: t}
}

func (p *fakePeers) Credentials(net.Conn) (*unix.Ucred, error) {
	p.trace = append(p.trace, "cred")
	return &unix.Ucred{Uid: p.uid, Pid: 123}, nil
}

func (p *fakePeers) PIDFD(net.Conn) (int, error) {
	p.trace = append(p.trace, "open")
	p.open = true
	return 7, nil
}

func (p *fakePeers) held(fd int) {
	p.t.Helper()
	if !p.open || fd != 7 {
		p.t.Fatal("pidfd is not held")
	}
}

func (p *fakePeers) FDInfo(fd int) ([]byte, error) {
	p.held(fd)
	p.trace = append(p.trace, "info")
	return []byte(p.info), nil
}

func (p *fakePeers) Status(pid int) ([]byte, error) {
	p.held(7)
	if pid != 123 {
		p.t.Fatal(pid)
	}
	p.trace = append(p.trace, "status")
	return []byte(p.status), nil
}

func (p *fakePeers) Threads(pid int) ([]string, error) {
	p.held(7)
	if pid != 123 {
		p.t.Fatal(pid)
	}
	p.trace = append(p.trace, "threads")
	return p.threads, nil
}

func (p *fakePeers) Cgroup(pid int, tid string) ([]byte, error) {
	p.held(7)
	if pid != 123 {
		p.t.Fatal(pid)
	}
	p.trace = append(p.trace, "group/"+tid)
	if tid == "124" {
		return []byte(p.workerGroup), nil
	}
	return []byte(p.group), nil
}

func (p *fakePeers) Alive(fd int) (bool, error) {
	p.held(fd)
	p.trace = append(p.trace, "alive")
	return p.alive, nil
}

func (p *fakePeers) CloseFD(fd int) error {
	p.held(fd)
	p.trace = append(p.trace, "close")
	p.open = false
	return nil
}

type memoryConn struct {
	net.Conn
	in            *bytes.Reader
	out           bytes.Buffer
	reads, writes int
	closed        bool
	write         func([]byte) (int, error)
}

func mem(b []byte) *memoryConn { return &memoryConn{in: bytes.NewReader(b)} }

func (c *memoryConn) Read(b []byte) (int, error) { c.reads++; return c.in.Read(b) }

func (c *memoryConn) Write(b []byte) (int, error) {
	c.writes++
	if c.write != nil {
		return c.write(b)
	}
	return c.out.Write(b)
}

func (c *memoryConn) Close() error { c.closed = true; return nil }

func (c *memoryConn) SetReadDeadline(time.Time) error { return nil }

func (c *memoryConn) SetDeadline(time.Time) error { return nil }

func fixture(t *testing.T) *Port {
	t.Helper()
	p, err := New(Config{Group: "/unit", SessionForm: "printable", Peers: peers(t),
		Table: hookwire.Table{{ID: "start", Event: "start", Output: "none", Fields: []pack.HookField{{Name: "session"}}}, {ID: "other", Event: "other", Fields: []pack.HookField{{Name: "session"}}}, {ID: "end", Output: "continuation"}},
		Decode: func(row pack.HookRow, r hookwire.Request, _ []byte) (agentsession.HookReport, string, error) {
			k := agentsession.HookKind("other")
			if row.Event == "start" {
				k = "session-start"
			}
			return agentsession.HookReport{Kind: k, Session: r.Fields["session"]}, strings.Repeat("a", 1025), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func request(t *testing.T, row, session string, cut bool) []byte {
	t.Helper()
	r := hookwire.Request{Version: 1, Kind: hookwire.Event, Row: row, Fields: map[string]string{}, Digests: map[string]string{}, Size: 123, Cut: cut}
	if session != "" {
		r.Fields["session"] = session
	}
	if row == "end" {
		r.Kind = hookwire.Fetch
	}
	b, err := hookwire.EncodeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runOne(p *Port, c net.Conn) { p.handle(context.Background(), c, p.cfg.Clock.Now()) }

func TestDecoderAliasesSurviveNextConnection(t *testing.T) {
	p := fixture(t)
	p.cfg.Decode = func(_ pack.HookRow, r hookwire.Request, frame []byte) (agentsession.HookReport, string, error) {
		value := r.Fields["session"]
		i := bytes.Index(frame, []byte(value))
		if i < 0 {
			t.Fatal("fixture session missing from frame")
		}
		session := unsafe.String(&frame[i], len(value))
		kind, _, _ := strings.Cut(session, "/")
		return agentsession.HookReport{Kind: agentsession.HookKind(kind), Session: session}, session, nil
	}
	const first = "session-start/one"
	runOne(p, mem(request(t, "start", first, false)))
	before := p.Snapshot()
	runOne(p, mem(request(t, "start", "config-change/two", false)))
	after := p.Snapshot()
	if len(before.Events) != 1 || len(after.Events) != 2 {
		t.Fatal("expected both connections to record events", before, after)
	}
	for _, snapshot := range []Snapshot{before, after} {
		ev := snapshot.Events[0]
		if snapshot.Bound != first || ev.Report.Kind != "session-start" || ev.Report.Session != first || ev.Text != first {
			t.Fatalf("stored report changed after buffer reuse: %+v", snapshot)
		}
	}
	if after.Events[1].Class != "foreign" {
		t.Fatal("second report used a rewritten binding", after)
	}
}

func TestHP4AdmissionBeforeRead(t *testing.T) {
	for _, tc := range []struct {
		name, record string
		ok           bool
	}{
		{"equal", "0::/unit\n", true}, {"child", "0::/unit/agent\n", true}, {"prefix neighbour", "0::/unit-x\n", false},
		{"other", "0::/other\n", false}, {"ancestor", "0::/\n", false}, {"relative", "0::unit\n", false}, {"dot", "0::/unit/../other\n", false},
		{"empty component", "0::/unit//other\n", false}, {"hybrid", "1:name:/unit\n0::/unit\n", false},
		{"second unified", "0::/unit\n0::/other\n", false}, {"oversize", "0::/unit/" + strings.Repeat("x", 4096), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t)
			peer := p.cfg.Peers.(*fakePeers)
			peer.group = tc.record
			c := mem(request(t, "end", "", false))
			runOne(p, c)
			if !c.closed || peer.open {
				t.Fatal("leaked connection or pidfd")
			}
			if tc.ok {
				if !slices.Equal(peer.trace, []string{"cred", "open", "info", "status", "threads", "group/123", "group/124", "alive", "close"}) || c.writes != 1 {
					t.Fatal(peer.trace, c.writes)
				}
			} else if c.reads != 0 || c.writes != 0 || p.Snapshot().Counters.Peer != 1 {
				t.Fatal("refused peer had I/O", c.reads, c.writes)
			}
		})
	}
	for _, why := range []string{"uid", "dead", "worker outside unit", "missing leader", "zombie", "pid mismatch", "duplicate pid", "oversize fdinfo"} {
		t.Run(why, func(t *testing.T) {
			p := fixture(t)
			peer := p.cfg.Peers.(*fakePeers)
			switch why {
			case "worker outside unit":
				peer.workerGroup = "0::/other"
			case "missing leader":
				peer.threads = []string{"124"}
			case "zombie":
				peer.status = "State: Z (zombie)"
			case "uid":
				peer.uid++
			case "dead":
				peer.alive = false
			case "pid mismatch":
				peer.info = "Pid: 99"
			case "duplicate pid":
				peer.info += peer.info
			case "oversize fdinfo":
				peer.info += strings.Repeat("x", 4096)
			}
			c := mem(nil)
			runOne(p, c)
			if peer.open || c.reads != 0 || !c.closed || p.Snapshot().Counters.Peer != 1 {
				t.Fatal(peer.trace)
			}
		})
	}
}

func TestHP3ForgedEventsBoundedAndLabelled(t *testing.T) {
	p := fixture(t)
	for range ringSize + 1 {
		c := mem(request(t, "start", "made-up", false))
		runOne(p, c)
		if c.writes != 0 {
			t.Fatal("answered event")
		}
	}
	runOne(p, mem(request(t, "start", "", true)))
	s := p.Snapshot()
	if len(s.Events) != ringSize || s.Counters.Dropped != 2 || s.Counters.Cut != 1 {
		t.Fatal(s.Counters)
	}
	last := s.Events[ringSize-1]
	if !last.Cut || last.Size != 123 || last.Report.Session != "" || last.Text != "" {
		t.Fatal(last)
	}
	for _, ev := range s.Events[:ringSize-1] {
		if ev.Source != Source || len(ev.Text) != 1024 {
			t.Fatal(ev)
		}
	}
}

func TestHP3ForgedHelperInputHasNoPermit(t *testing.T) {
	tables, err := hookwire.DecodeTable(packhook.RowsJSON())
	if err != nil {
		t.Fatal(err)
	}
	for build, rows := range tables {
		t.Run(build, func(t *testing.T) {
			var claim agentsession.Claim
			p, err := New(Config{
				Group: "/unit", SessionForm: "printable", Peers: peers(t), Table: rows,
				Decode: func(_ pack.HookRow, _ hookwire.Request, frame []byte) (agentsession.HookReport, string, error) {
					o, err := packhook.Decode(build, frame)
					if err != nil {
						return agentsession.HookReport{}, "", err
					}
					claim = o.Claim
					return o.Report, string(claim.State), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Event != "SessionStart" && row.Event != "Stop" {
					continue
				}
				// Invoke the helper's real extractor with a fabricated provider input.
				input := `{"session_id":"made-up","hook_event_name":"` + row.Event + `"}`
				req, err := hookwire.Extract(row, strings.NewReader(input))
				if err != nil {
					t.Fatal(err)
				}
				frame, err := hookwire.EncodeRequest(*req)
				if err != nil {
					t.Fatal(err)
				}
				runOne(p, mem(frame))
			}
			s := p.Snapshot()
			if len(s.Events) != 2 || s.Bound != "made-up" || s.Events[1].Class != "bound" || claim.State != "idle" {
				t.Fatal(s, claim)
			}
			record := agentsession.Compose(agentsession.Inputs{
				Transport: agentsession.Claim{State: "unknown", Basis: "transport"},
				Hook:      &claim, Sight: "rules",
			})
			if record.State != "idle" || record.Deliverable || record.Permit != "" {
				t.Fatal(record)
			}
		})
	}
}

type queuedPeers struct {
	*fakePeers
	checkedHost bool
}

func (p *queuedPeers) Cgroup(pid int, tid string) ([]byte, error) {
	record, err := p.fakePeers.Cgroup(pid, tid)
	if !p.checkedHost {
		p.checkedHost = true
		time.Sleep(15 * time.Millisecond)
		return []byte("0::/host-session\n"), nil
	}
	return record, err
}

type shiftedClock struct{ wallClock }

func (shiftedClock) Now() time.Time { return time.Now().Add(-time.Hour) }

func TestHP4PendingHostAheadOfHelper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, l := fixture(t), newQueue()
		p.cfg.Peers = &queuedPeers{fakePeers: peers(t)}
		p.cfg.Clock = shiftedClock{}
		queued := p.cfg.Clock.Now()
		host, helper := mem(request(t, "end", "", false)), mem(request(t, "end", "", false))
		l.pending <- host
		l.pending <- helper
		stop := loop(t, p, l)
		defer stop()
		time.Sleep(15 * time.Millisecond)
		synctest.Wait()
		s := p.Snapshot()
		if host.reads != 0 || host.writes != 0 || !host.closed || s.Counters.Peer != 1 || helper.writes != 1 || len(s.Events) != 1 {
			t.Fatal("pending host/helper admission", s)
		}
		if delay := s.Events[0].At.Sub(queued); delay != 15*time.Millisecond {
			t.Fatal("helper queue delay", delay)
		}
	})
}

func TestHP9ForgedFirstBinding(t *testing.T) {
	p := fixture(t)
	i := sessiontest.Alpha().Independent()
	i.I1 = false
	before, err := agentsession.Readiness(i, p.binding)
	if err != nil {
		t.Fatal(err)
	}
	runOne(p, mem(request(t, "start", "made-up", false)))
	runOne(p, mem(request(t, "start", "provider", false)))
	runOne(p, mem(request(t, "other", "provider", false)))
	s := p.Snapshot()
	if s.Bound != "made-up" || s.Events[0].Class != "bound" || s.Events[1].Class != "nested" || s.Events[2].Class != "foreign" || s.Counters.Nested != 1 || s.Counters.Foreign != 1 {
		t.Fatal(s)
	}
	after, err := agentsession.Readiness(i, p.binding)
	if err != nil || !reflect.DeepEqual(after.Delivery, before.Delivery) || after.Deny != before.Deny {
		t.Fatal(after, err)
	}
}

func TestFrameRefusalsAndSingleExchange(t *testing.T) {
	p := fixture(t)
	fetch := request(t, "end", "", false)
	for _, frame := range [][]byte{binary.BigEndian.AppendUint32(nil, 8193), {0, 0, 0, 1, '{'}, fetch[:6], request(t, "missing", "", false)} {
		c := mem(frame)
		runOne(p, c)
		if c.writes != 0 || !c.closed {
			t.Fatal("refusal wrote or leaked")
		}
	}
	c := mem(append(slices.Clone(fetch), fetch...))
	runOne(p, c)
	s := p.Snapshot()
	if s.Counters.Size != 1 || s.Counters.Malformed != 2 || s.Counters.Row != 1 || c.in.Len() != len(fetch) || c.writes != 1 || !c.closed {
		t.Fatal(s, c)
	}
	reply, err := hookwire.DecodeReply(c.out.Bytes())
	if err != nil || reply.Kind != hookwire.None {
		t.Fatal(reply, err)
	}
}

func TestWriteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		n     int
		delay time.Duration
		state string
		late  bool
	}{
		{"zero", 0, 0, "write-uncertain", false}, {"partial", 3, 0, "write-uncertain", false}, {"whole", -1, 0, "handed-out", false}, {"late whole", -1, exchange + 1, "handed-out", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := fixture(t)
				c := mem(request(t, "end", "", false))
				c.write = func(b []byte) (int, error) {
					if p.Snapshot().Write.State != "writing" {
						t.Fatal("write not announced")
					}
					time.Sleep(tc.delay)
					if tc.late {
						synctest.Wait()
						if !c.closed {
							t.Fatal("still open past deadline")
						}
					}
					if tc.n < 0 {
						return len(b), nil
					}
					return tc.n, io.ErrShortWrite
				}
				runOne(p, c)
				s := p.Snapshot()
				wantBytes := tc.n
				if wantBytes < 0 {
					wantBytes = len(p.none)
				}
				if s.Write.Bytes != wantBytes || !s.Write.At.Equal(time.Now()) {
					t.Fatal("write evidence", s.Write)
				}
				if s.Write.State != tc.state || s.Write.Late != tc.late || c.writes != 1 || !c.closed {
					t.Fatal(s.Write)
				}
			})
		})
	}
}

type queue struct {
	net.Listener
	pending chan net.Conn
	closed  chan struct{}
	once    sync.Once
	accepts atomic.Int64
}

func newQueue() *queue { return &queue{pending: make(chan net.Conn, 120), closed: make(chan struct{})} }

func (l *queue) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.pending:
		return c, nil
	}
}

func (l *queue) Close() error { l.once.Do(func() { close(l.closed) }); return nil }

func loop(t *testing.T, p *Port, l *queue) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := p.Serve(ctx, l, Filtered{digest: [32]byte{1}})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("serve: %v", err)
		}
	}()
	return cancel
}

func TestHP5RateBeforeAccept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fixture(t)
		l := newQueue()
		for range 110 {
			l.pending <- mem(nil)
		}
		stop := loop(t, p, l)
		synctest.Wait()
		if l.accepts.Load() != 100 || len(l.pending) != 10 {
			t.Fatal(l.accepts.Load(), len(l.pending))
		}
		time.Sleep(tokenPeriod - 1)
		synctest.Wait()
		if l.accepts.Load() != 100 {
			t.Fatal("accepted while paused")
		}
		time.Sleep(1)
		synctest.Wait()
		if l.accepts.Load() != 101 || p.Snapshot().Counters.Paused != tokenPeriod {
			t.Fatal(l.accepts.Load(), p.Snapshot())
		}
		stop()
		synctest.Wait()
		if l.accepts.Load() != 101 {
			t.Fatal("stop needed an accept")
		}
	})
}

func TestHP5BurstAfterIdleAccept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, l := fixture(t), newQueue()
		stop := loop(t, p, l)
		defer stop()
		synctest.Wait()
		time.Sleep(time.Minute)
		for range burst + 1 {
			l.pending <- mem(nil)
		}
		synctest.Wait()
		if p.Snapshot().Counters.Accepted != burst || len(l.pending) != 1 {
			t.Fatal("idle accept exceeded burst", p.Snapshot().Counters)
		}
	})
}

func TestHP5SnapshotAndStopWithStalledRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, l := fixture(t), newQueue()
		server, client := net.Pipe()
		defer client.Close()
		l.pending <- server
		stop := loop(t, p, l)
		synctest.Wait()
		start := time.Now()
		if got := p.Snapshot().Counters.Accepted; got != 1 {
			t.Fatal(got)
		}
		stop()
		synctest.Wait()
		if _, err := client.Read(make([]byte, 1)); err != io.EOF || !time.Now().Equal(start) {
			t.Fatal("control waited for the hook deadline", err)
		}
	})
}

func TestHP6HeldConnectionAndHelperBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fixture(t)
		l := newQueue()
		srv, peer := net.Pipe()
		defer peer.Close()
		l.pending <- srv
		stop := loop(t, p, l)
		defer stop()
		if _, err := peer.Write([]byte{0}); err != nil {
			t.Fatal(err)
		} // a caller that keeps no deadline
		waiting, helper := net.Pipe()
		l.pending <- waiting
		start := time.Now()
		_ = helper.SetDeadline(start.Add(300 * time.Millisecond))
		if _, err := helper.Write(request(t, "other", "s", false)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
		helper.Close()
		if time.Since(start) != 300*time.Millisecond || l.accepts.Load() != 1 {
			t.Fatal("helper waited for holder")
		}
		time.Sleep(exchange - 300*time.Millisecond)
		synctest.Wait()
		if p.Snapshot().Counters.Deadline != 1 {
			t.Fatal(p.Snapshot())
		}
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
			t.Fatal(err)
		}
	})
}

func TestFirstByteTimeoutAndNoLateReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fixture(t)
		srv, peer := net.Pipe()
		defer peer.Close()
		go runOne(p, srv)
		start := time.Now()
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF || time.Since(start) != firstByte {
			t.Fatal(err, time.Since(start))
		}
		synctest.Wait()
		c := mem(nil)
		if got := p.reply(c, time.Now()); got != "deadline" || c.writes != 0 || p.Snapshot().Write.State != "not-started" {
			t.Fatal(got, c.writes)
		}
	})
}
