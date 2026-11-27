package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	a "ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/enrollment"
	"ataniq/internal/seatgate"
)

type pilot struct {
	path string
	c    config
	p    pairing
	b    enrollment.Binding
	w    *cp.Memory
	tls  seatgate.Identity
}

func newPilot(t *testing.T) *pilot {
	t.Helper()
	path, _, key, tls := ceremonyConfig(t)
	call(t, path, "identity-init")
	c, err := loadConfig(path, fullPhase)
	must(t, err)
	p, err := loadPairing(c)
	must(t, err)
	w, err := cp.NewMemory(c.Slot.Witness, c.Epoch, key)
	must(t, err)
	must(t, w.Grant(c.Slot, p.home.key.Public().(ed25519.PublicKey), true))
	b, err := enrollment.Enroll(t.Context(), enrollment.FileRoot{Path: c.State}, w.Administration(), c.Slot, c.Epoch, p.home.key, p.witness, nil)
	must(t, err)
	must(t, enrollment.WriteBinding(c.Binding, b))
	return &pilot{path, c, p, b, w, tls}
}
func (f *pilot) state(t *testing.T) cp.State {
	t.Helper()
	q := cp.ReadRequest{Slot: f.c.Slot}
	rand.Read(q.Challenge[:])
	v, err := f.w.Administration().Read(t.Context(), q)
	must(t, err)
	must(t, cp.VerifyView(f.p.witness, q, v))
	return v.Current
}
func (f *pilot) mutation(t *testing.T) cp.Mutation {
	m := cp.Mutation{Expected: f.state(t)}
	rand.Read(m.ID[:])
	return m
}
func (f *pilot) release(t *testing.T) {
	t.Helper()
	_, err := f.w.Administration().Release(t.Context(), f.mutation(t))
	must(t, err)
}
func (f *pilot) run(t *testing.T, cmd command, fault enrollment.Fault) (string, error) {
	t.Helper()
	l := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	ep, err := homeEndpoint(f.c, f.p)
	must(t, err)
	remote := link.Remote{Address: f.c.Listen, ServerPin: f.p.home.tls.Fingerprint(), DialContext: l.dial}
	server, err := link.NewServer(f.tls, f.p.witness, remote, f.c.Slot)
	must(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	done := make(chan error, 1)
	go func() { done <- server.Poll(ctx, memoryWitness{f.w}) }()
	var out bytes.Buffer
	err = observe(ctx, cmd, f.c, f.p, ep, l, &out, fault)
	cancel()
	if pollErr := <-done; !errors.Is(pollErr, context.Canceled) {
		t.Fatal(pollErr)
	}
	verdict := strings.Contains(out.String(), "observation: ")
	if strings.HasSuffix(out.String(), "managed_starts=0 launches=0\n") != verdict {
		t.Fatal(out.String())
	}
	return out.String(), err
}
func TestObserveArchive(t *testing.T) {
	f := newPilot(t)
	cmd, ok := parseCommand([]string{"observe", "--config", f.path})
	if !ok || cmd.events != 3 {
		t.Fatal(cmd, ok)
	}
	out, err := f.run(t, cmd, nil)
	must(t, err)
	head := headOf(f.state(t).Head)
	if head.Sequence != 3 || !strings.Contains(out, "home "+head.String()+"\nwitness "+head.String()+" (challenged "+head.String()+")\narchive absent\nobservation: agree\n") {
		t.Fatal(out)
	}
	path := filepath.Join(filepath.Dir(f.path), "archive")
	cmd.events, cmd.archive = 0, path
	must(t, enrollment.WriteHeadArchive(path, f.state(t).Head))
	f.release(t)
	out, err = f.run(t, cmd, nil)
	must(t, err)
	if !strings.Contains(out, "archive "+head.String()+"\nobservation: agree\n") {
		t.Fatal(out)
	}
	head.Sequence--
	must(t, os.WriteFile(path, []byte(head.String()+"\n"), 0600))
	f.release(t)
	out, err = f.run(t, cmd, nil)
	if err == nil || !strings.Contains(out, "observation: DISAGREE home/archive, witness receipt/archive\n") {
		t.Fatal(out, err)
	}
	info, err := os.Stat(filepath.Join(f.c.State, observationFile))
	must(t, err)
	if info.Mode() != 0600 {
		t.Fatal(info.Mode())
	}
}

type observeFault func(string) error

func (f observeFault) After(point string) error { return f(point) }

func TestObserveReportsInterruptedAdvance(t *testing.T) {
	f := newPilot(t)
	cut := errors.New("lost before receipt save")
	fault := observeFault(func(point string) error {
		if point == "advance.committed" {
			return cut
		}
		return nil
	})
	_, err := f.run(t, command{events: 2}, fault)
	var refusal *enrollment.Error
	if !errors.Is(err, cut) || !errors.As(err, &refusal) || refusal.Reason != enrollment.Interrupted ||
		!strings.Contains(err.Error(), "owner action: preserve the staged intent") || f.state(t).Head.Sequence != 1 {
		t.Fatal(err, f.state(t))
	}
	// Only the independent owner can release the interrupted writer.
	f.release(t)
	out, err := f.run(t, command{events: 0}, nil)
	must(t, err)
	if !strings.Contains(out, "observation: agree\n") || f.state(t).Head.Sequence != 1 {
		t.Fatal(out, f.state(t))
	}
}
func TestObserveRestartBeforeAppend(t *testing.T) {
	f := newPilot(t)
	cut := errors.New("stopped after staging")
	fault := observeFault(func(point string) error {
		if point == "stage.dir-synced" {
			return cut
		}
		return nil
	})
	_, err := f.run(t, command{events: 2}, fault)
	if !errors.Is(err, cut) || f.state(t).Head.Sequence != 0 {
		t.Fatal(err, f.state(t).Head)
	}
	stage := filepath.Join(f.c.State, "advance.pending")
	before := read(t, stage)
	f.release(t)
	ep, err := homeEndpoint(f.c, f.p)
	must(t, err)
	if pending, ok := ep.PendingMutation(); ok {
		t.Fatal("unappended intent blocks claim", pending)
	}
	if !bytes.Equal(before, read(t, stage)) {
		t.Fatal("endpoint changed stage before claim")
	}
	cmd, ok := parseCommand([]string{"observe", "--config", f.path})
	if !ok {
		t.Fatal("invalid observe command")
	}
	out, err := f.run(t, cmd, nil)
	must(t, err)
	if !strings.Contains(out, "observation: agree\n") || f.state(t).Head.Sequence != uint64(cmd.events) {
		t.Fatal(out, f.state(t).Head)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stage after recovery", err)
	}
}

func TestObserveRefusesRestores(t *testing.T) {
	for _, reason := range []enrollment.Reason{enrollment.HomeBehind, enrollment.HomeAhead, enrollment.Fork, enrollment.Occupied, enrollment.Retired} {
		t.Run(string(reason), func(t *testing.T) {
			f := newPilot(t)
			path := filepath.Join(f.c.State, "events.v1.jsonl")
			header := read(t, path)
			switch reason {
			case enrollment.HomeAhead:
				ledger, err := a.NewLedgerFromHeader(header)
				must(t, err)
				signer, err := wire.NewSigner(f.b.Header, f.p.home.key)
				must(t, err)
				event, err := mirrorEvent(ledger, f.b.Header, signer)
				must(t, err)
				frame, err := event.RecordBytes()
				must(t, err)
				must(t, os.WriteFile(path, append(header, frame...), 0600))
			case enrollment.Retired:
				_, err := f.run(t, command{events: 1}, nil)
				must(t, err)
				f.release(t)
				_, err = f.w.Administration().Retire(t.Context(), cp.Retirement{Mutation: f.mutation(t), DispositionDigest: strings.Repeat("b", 64)})
				must(t, err)
			default:
				_, err := f.run(t, command{events: 1}, nil)
				must(t, err)
				switch reason {
				case enrollment.HomeBehind:
					must(t, os.WriteFile(path, header, 0600))
				case enrollment.Fork:
					data := read(t, path)
					e, err := a.DecodeRecord(data[len(header):])
					must(t, err)
					ledger, err := a.NewLedgerFromHeader(header)
					must(t, err)
					c := e.Command
					c.RequestID = "other-branch"
					other, _, err := a.Decide(ledger, c)
					must(t, err)
					signer, err := wire.NewSigner(f.b.Header, f.p.home.key)
					must(t, err)
					body, err := other.SigningBytes()
					must(t, err)
					other.Signature, err = signer.Sign(f.b.Header, body)
					must(t, err)
					raw, err := other.RecordBytes()
					must(t, err)
					must(t, os.WriteFile(path, append(header, raw...), 0600))
				}
			}
			if reason == enrollment.HomeBehind || reason == enrollment.Fork {
				if status := call(t, f.path, "status"); !strings.Contains(status, "last_observation=agree (stale: ledger head moved)\n") {
					t.Fatal(status)
				}
			}
			before := read(t, path)
			out, err := f.run(t, command{events: 1}, nil)
			var e *enrollment.Error
			if !errors.As(err, &e) || e.Reason != reason || !strings.Contains(err.Error(), "\nowner action: ") || strings.Contains(out, "observation: agree") || !bytes.Equal(before, read(t, path)) {
				t.Fatal(err, out)
			}
			if reason != enrollment.Occupied {
				if status := call(t, f.path, "status"); !strings.Contains(status, "last_observation=refused: "+string(reason)+"\n") {
					t.Fatal(status)
				}
			}
		})
	}
}
func TestObserveArchiveFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive")
	good := "sequence=1 head=" + strings.Repeat("a", 64) + "\n"
	for _, bad := range []string{good + "\n", strings.TrimSpace(good), strings.Replace(good, "1", "01", 1), strings.ToUpper(good), good + good, " " + good, strings.Replace(good, " ", "  ", 1)} {
		must(t, os.WriteFile(path, []byte(bad), 0600))
		if _, err := readArchive(path); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestObserveDecisionInhibit(t *testing.T) {
	f := newPilot(t)
	f.w.Inhibit(true)
	out, err := f.run(t, command{events: 1}, nil)
	if out != "" || !errors.Is(err, link.ErrInhibited) ||
		!strings.Contains(err.Error(), "held decision inhibit with ah-switch status. New enrollment and observation require a clear inhibit. Keep managed admission closed") {
		t.Fatal(out, err)
	}
	f.w.Inhibit(false)
	if st := f.state(t); st.Head.Sequence != 0 || st.SessionKeyID != "" {
		t.Fatal("inhibited observation changed slot", st)
	}
}

func TestObservePartition(t *testing.T) {
	f := newPilot(t)
	ep, err := homeEndpoint(f.c, f.p)
	must(t, err)
	l := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	err = observe(ctx, command{events: 1}, f.c, f.p, ep, l, &out, nil)
	var e *enrollment.Error
	if !errors.As(err, &e) || e.Reason != enrollment.Unreachable || !strings.Contains(err.Error(), "owner action: restore witness polling") || f.state(t).Head.Sequence != 0 || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
	result, err := f.run(t, command{events: 1}, nil)
	must(t, err)
	if !strings.Contains(result, "observation: agree\n") {
		t.Fatal(result)
	}
}
