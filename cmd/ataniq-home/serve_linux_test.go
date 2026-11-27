package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
)

type memoryWitness struct{ *cp.Memory }

func (w memoryWitness) CheckHome(ctx context.Context, slot cp.Slot, key ed25519.PublicKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.Memory.CheckHome(slot, key)
}

type pipeListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case l.incoming <- b:
		return a, nil
	case <-ctx.Done():
	case <-l.closed:
	}
	a.Close()
	b.Close()
	return nil, net.ErrClosed
}

// Check the actual six-byte v2 Idle response before using the memory witness's
// normal poll loop and signed, pre-enrollment grant check.
func TestServeBeforeEnrollment(t *testing.T) {
	path, m, witness, witnessTLS := ceremonyConfig(t)
	call(t, path, "identity-init")
	c, err := loadConfig(path, fullPhase)
	must(t, err)
	p, err := loadPairing(c)
	must(t, err)
	ep, err := homeEndpoint(c, p)
	must(t, err)
	l := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var log bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ep, l, &log) }()
	defer func() {
		cancel()
		must(t, <-done)
		if log.String() != "home serve started\nhome serve stopped\n" {
			t.Error(log.String())
		}
	}()
	raw, err := l.dial(ctx, "", "")
	must(t, err)
	defer raw.Close()
	deadline, _ := ctx.Deadline()
	must(t, raw.SetDeadline(deadline))
	cfg := seatgate.ClientTLS(witnessTLS, p.home.tls.Fingerprint(), "")
	cfg.NextProtos = []string{link.ALPN}
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if s.NegotiatedProtocol != link.ALPN {
			return errors.New("unexpected link protocol")
		}
		return nil
	}
	conn := tls.Client(raw, cfg)
	_, err = conn.Write([]byte{0, 0, 0, 2, 2, 15}) // Poll
	must(t, err)
	var idle [6]byte
	_, err = io.ReadFull(conn, idle[:])
	must(t, err)
	if idle != [6]byte{0, 0, 0, 2, 2, 16} {
		t.Fatalf("idle reply %x", idle)
	}
	raw.Close()

	w, err := cp.NewMemory(c.Slot.Witness, c.Epoch, witness)
	must(t, err)
	must(t, w.Grant(c.Slot, p.home.key.Public().(ed25519.PublicKey), true))
	remote := link.Remote{Address: c.Listen, ServerPin: p.home.tls.Fingerprint(), DialContext: l.dial}
	server, err := link.NewServer(witnessTLS, p.witness, remote, c.Slot)
	must(t, err)
	pollCtx, stop := context.WithCancel(ctx)
	polling := make(chan error, 1)
	go func() { polling <- server.Poll(pollCtx, memoryWitness{w}) }()
	defer func() {
		stop()
		if err := <-polling; !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	_, session, err := ed25519.GenerateKey(nil)
	must(t, err)
	_, err = ep.Connect(ctx, c.Slot, p.home.key, session)
	must(t, err)
	if got := names(t, m["state"].(string)); len(got) != 0 {
		t.Fatalf("created state while serving: %v", got)
	}
}

func TestServeCheckOccupiedPort(t *testing.T) {
	path, m, _, _ := ceremonyConfig(t)
	call(t, path, "identity-init")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("loopback TCP listener denied: %v", err)
	}
	must(t, err)
	defer l.Close()
	m["listen"] = l.Addr().String()
	writeConfig(t, path, m)
	got, _ := cli(t, 0, "serve", "--check", "--config", path)
	if got != "home check ok\n" {
		t.Fatal(got)
	}
	_, diag := cli(t, 1, "serve", "--config", path)
	if !strings.Contains(diag, "listen") {
		t.Fatal(diag)
	}
}

func TestServeSIGTERM(t *testing.T) {
	const childConfig = "ATANIQ_TEST_HOME_SERVE_CONFIG"
	const pipeMode = "ATANIQ_TEST_HOME_PIPE"
	if path := os.Getenv(childConfig); path != "" {
		if os.Getenv(pipeMode) == "" {
			os.Exit(run([]string{"serve", "--config", path}, os.Stdout, os.Stderr, os.Geteuid()))
		}
		c, err := loadConfig(path, fullPhase)
		must(t, err)
		p, err := loadPairing(c)
		must(t, err)
		ep, err := homeEndpoint(c, p)
		must(t, err)
		must(t, listenAndServe(c.Listen, ep, os.Stdout, func(_, _ string) (net.Listener, error) {
			return &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}, nil
		}))
		os.Exit(0)
	}
	path, m, _, _ := ceremonyConfig(t)
	call(t, path, "identity-init")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	pipe := errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
	if pipe {
		t.Log("loopback TCP denied; delivering SIGTERM to the home serving a pipe listener")
	} else {
		must(t, err)
		m["listen"] = l.Addr().String()
		writeConfig(t, path, m)
		must(t, l.Close())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServeSIGTERM$")
	cmd.Env = append(os.Environ(), childConfig+"="+path)
	if pipe {
		cmd.Env = append(cmd.Env, pipeMode+"=1")
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	must(t, err)
	if line != "home serve started\n" {
		t.Fatal(line)
	}
	// Leave a pre-authentication worker open while shutdown joins workers.
	if !pipe {
		peer, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
		must(t, err)
		defer peer.Close()
	}
	must(t, cmd.Process.Signal(syscall.SIGTERM))
	rest, err := io.ReadAll(reader)
	must(t, err)
	err = cmd.Wait()
	stopped = true
	if err != nil || string(rest) != "home serve stopped\n" || diagnostic.Len() != 0 {
		t.Fatalf("shutdown: %v, stdout %q, stderr %q", err, rest, diagnostic.String())
	}
}

func TestServeCheckRefusesDamagedStage(t *testing.T) {
	path, m, _, _ := ceremonyConfig(t)
	call(t, path, "identity-init")
	stage := filepath.Join(m["state"].(string), "advance.pending")
	must(t, os.WriteFile(stage, []byte("{"), 0600))
	_, diag := cli(t, 1, "serve", "--config", path, "--check")
	if !strings.Contains(diag, "pending advance") || string(read(t, stage)) != "{" {
		t.Fatalf("preflight: %s", diag)
	}
}
