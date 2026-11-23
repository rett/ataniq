package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/seatnotice"
	"ataniq/internal/webpush"
)

// TestMain lets a test run the command as a process: with
// ATANIQ_SEAT_NOTIFY_AS_MAIN set, the test binary is the command.
func TestMain(m *testing.M) {
	if os.Getenv("ATANIQ_SEAT_NOTIFY_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// shortDir makes a folder with a short path, for a Unix socket, that group and
// other cannot write.
func shortDir(t *testing.T) string {
	t.Helper()
	return ownDir(t)
}

// newKey makes a VAPID key file in a new folder, as vapid-init does.
func newKey(t *testing.T, dir string) (path, public string) {
	t.Helper()
	path = filepath.Join(dir, "vapid.pem")
	var out, errout bytes.Buffer
	if code := vapidInit([]string{"--key", path}, &out, &errout); code != 0 {
		t.Fatalf("vapid-init %d: %s", code, errout.String())
	}
	return path, strings.TrimSpace(out.String())
}

func TestVapidInit(t *testing.T) {
	dir := ownDir(t)
	path := filepath.Join(dir, "vapid.pem")
	var out, errout bytes.Buffer
	if code := vapidInit([]string{"--key", path}, &out, &errout); code != 0 {
		t.Fatalf("exit %d: %s", code, errout.String())
	}
	// The public key and a line end, nothing else.
	pub := out.String()
	if !strings.HasSuffix(pub, "\n") || strings.Count(pub, "\n") != 1 || !seatnotice.ValidVAPIDPublic(strings.TrimSpace(pub)) {
		t.Fatalf("output %q", pub)
	}
	if errout.Len() != 0 {
		t.Errorf("stderr %q", errout.String())
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	k, err := webpush.LoadVAPIDKeyFile(path)
	if err != nil || k.PublicKeyString() != strings.TrimSpace(pub) {
		t.Fatalf("the file holds another key: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("PRIVATE")) {
		t.Error("the private key was printed")
	}

	// It never overwrites.
	before, _ := os.ReadFile(path)
	out.Reset()
	errout.Reset()
	if code := vapidInit([]string{"--key", path}, &out, &errout); code != 1 {
		t.Fatalf("second run: exit %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("the second run printed %q", out.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("the key was overwritten")
	}
	// Nor through a link.
	link := filepath.Join(dir, "link.pem")
	os.Symlink(path, link)
	if code := vapidInit([]string{"--key", link}, &out, &errout); code != 1 {
		t.Errorf("a link: exit %d", code)
	}

	for name, args := range map[string][]string{
		"no key":         {},
		"relative":       {"--key", "vapid.pem"},
		"an argument":    {"--key", filepath.Join(dir, "x.pem"), "extra"},
		"an unknown one": {"--key", filepath.Join(dir, "x.pem"), "--force"},
	} {
		out.Reset()
		errout.Reset()
		if code := vapidInit(args, &out, &errout); code != 2 || out.Len() != 0 {
			t.Errorf("%s: exit %d, output %q", name, code, out.String())
		}
	}
	// A folder others can write is refused.
	wide := ownDir(t)
	os.Chmod(wide, 0o777)
	if code := vapidInit([]string{"--key", filepath.Join(wide, "k.pem")}, &out, &errout); code != 1 {
		t.Errorf("a folder others write: exit %d", code)
	}
}

func TestParseRefusals(t *testing.T) {
	good := []string{
		"--socket", "/run/seat-notify/notify.sock", "--seat-user", "seat",
		"--vapid-key", "/var/lib/seat-notify/vapid.pem", "--subscriptions", "/var/lib/seat-notify/subscriptions.json",
		"--vapid-subject", "mailto:owner@example.org",
	}
	var errout bytes.Buffer
	o, code, ok := parse(good, &errout)
	if !ok || code != 0 {
		t.Fatalf("the good flags: %d %s", code, errout.String())
	}
	if strings.Join(o.hosts, ",") != "web.push.apple.com" || o.ttl != time.Hour {
		t.Errorf("defaults: %v %v", o.hosts, o.ttl)
	}
	with := func(extra ...string) []string { return append(append([]string(nil), good...), extra...) }
	without := func(flag string) []string {
		var out []string
		for i := 0; i < len(good); i += 2 {
			if good[i] != flag {
				out = append(out, good[i], good[i+1])
			}
		}
		return out
	}
	o, _, ok = parse(with("--push-hosts", "Push.Example.org, other.example.net", "--ttl", "90s"), &errout)
	if !ok || strings.Join(o.hosts, ",") != "push.example.org,other.example.net" || o.ttl != 90*time.Second {
		t.Errorf("hosts %v ttl %v ok %v", o.hosts, o.ttl, ok)
	}
	if o, _, ok := parse(with("--ttl", "0"), &errout); !ok || o.ttl != 0 {
		t.Errorf("a zero ttl: %v %v", o.ttl, ok)
	}
	for name, args := range map[string][]string{
		"no socket":           without("--socket"),
		"relative socket":     with("--socket", "notify.sock")[:0:0],
		"no seat user":        without("--seat-user"),
		"no key":              without("--vapid-key"),
		"no subscriptions":    without("--subscriptions"),
		"no subject":          without("--vapid-subject"),
		"a bare subject":      with("--vapid-subject", "owner@example.org"),
		"an http subject":     with("--vapid-subject", "http://example.org"),
		"an argument":         with("extra"),
		"an unknown flag":     with("--verbose"),
		"a wildcard host":     with("--push-hosts", "*.apple.com"),
		"an IP literal host":  with("--push-hosts", "192.0.2.1"),
		"a host with a port":  with("--push-hosts", "web.push.apple.com:443"),
		"a host with a path":  with("--push-hosts", "web.push.apple.com/x"),
		"an empty host":       with("--push-hosts", "web.push.apple.com,"),
		"no dot in the host":  with("--push-hosts", "localhost"),
		"an empty hosts list": with("--push-hosts", ""),
		"a negative ttl":      with("--ttl", "-1s"),
		"a ttl over 28 days":  with("--ttl", "700h"),
		"a bad ttl":           with("--ttl", "soon"),
	} {
		if name == "relative socket" {
			args = append([]string(nil), good...)
			args[1] = "notify.sock"
		}
		errout.Reset()
		if _, code, ok := parse(args, &errout); ok || code != 2 {
			t.Errorf("%s: accepted (code %d)", name, code)
		}
		if strings.Contains(errout.String(), "owner@example.org") {
			t.Errorf("%s: the diagnostic repeats the subject", name)
		}
	}
}

// running is a notifier started in the test, on a socket of its own.
type running struct {
	sock   string
	dir    string
	push   *push
	errout *syncBuffer
	stop   func() int
	subs   string
}

type syncBuffer struct {
	logs
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, string(p))
	return len(p), nil
}

// start runs the command with the fake service and this user as the seat
// user, extra flags after the usual ones.
func start(t *testing.T, p *push, uid uint32, extra ...string) *running {
	t.Helper()
	dir := shortDir(t)
	keyPath, _ := newKey(t, dir)
	r := &running{sock: filepath.Join(dir, "n.sock"), dir: dir, push: p, errout: &syncBuffer{}, subs: filepath.Join(dir, "subscriptions.json")}
	p.install(t, func(name string) (uint32, error) {
		if name != "seat" {
			return 0, user.UnknownUserError(name)
		}
		return uid, nil
	})
	args := append([]string{
		"--socket", r.sock, "--seat-user", "seat", "--vapid-key", keyPath, "--subscriptions", r.subs,
		"--vapid-subject", testSubject,
	}, extra...)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, r.errout, func(a net.Addr) { ready <- a }) }()
	select {
	case <-ready:
	case code := <-done:
		cancel()
		t.Fatalf("run exited %d: %s", code, r.errout.text())
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("run did not start")
	}
	var once bool
	r.stop = func() int {
		if once {
			return 0
		}
		once = true
		cancel()
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("run did not stop")
			return -1
		}
	}
	t.Cleanup(func() { r.stop() })
	return r
}

func (r *running) client(t *testing.T) *seatnotice.Client {
	t.Helper()
	c, err := seatnotice.NewClient(r.sock)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunServesTheSeatUser(t *testing.T) {
	p := newPush(t)
	r := start(t, p, uint32(os.Getuid()))
	ctx := context.Background()
	c := r.client(t)

	fi, err := os.Lstat(r.sock)
	if err != nil || fi.Mode().Type() != os.ModeSocket || fi.Mode().Perm() != 0o660 {
		t.Fatalf("the socket: %v %v", fi, err)
	}
	info, err := c.Info(ctx)
	if err != nil || info.Subscriptions != 0 || !seatnotice.ValidVAPIDPublic(info.VAPIDPublic) {
		t.Fatalf("info %+v: %v", info, err)
	}
	// Subscribe, notify, unsubscribe through the socket.
	s := p.subscription(t, "/p/phone")
	if err := c.Subscribe(ctx, s); err != nil {
		t.Fatal(err)
	}
	if sent, err := c.NotifySent(ctx, 4); sent != 1 || err != nil {
		t.Fatalf("sent %d: %v", sent, err)
	}
	seen := p.requests()
	if len(seen) != 1 || string(seen[0].payload) != `{"n":4}` || seen[0].header.Get("Topic") != "waiting" {
		t.Fatalf("the push service saw %+v", seen)
	}
	// What the service sees was signed by the key vapid-init made.
	if !strings.HasSuffix(seen[0].header.Get("Authorization"), ", k="+info.VAPIDPublic) {
		t.Errorf("Authorization %q", seen[0].header.Get("Authorization"))
	}
	if err := c.Subscribe(ctx, seatnotice.Subscription{Endpoint: "https://elsewhere.example/x", P256DH: s.P256DH, Auth: s.Auth}); err == nil {
		t.Error("a subscription on another host was kept")
	}
	// The file holds the subscription, mode 0600.
	if fi, err := os.Stat(r.subs); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("subscriptions file: %v %v", fi, err)
	}
	if err := c.Unsubscribe(ctx, seatnotice.EndpointSHA256(s.Endpoint)); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.Info(ctx); info.Subscriptions != 0 {
		t.Errorf("%d subscriptions", info.Subscriptions)
	}
	if err := c.Subscribe(ctx, s); err != nil {
		t.Fatal(err)
	}

	// send-test, through dispatch.
	var out, errout bytes.Buffer
	if code := dispatch(ctx, []string{"send-test", "--socket", r.sock, "3"}, &out, &errout); code != 0 || out.String() != "sent 1\n" {
		t.Fatalf("send-test: %d %q %q", code, out.String(), errout.String())
	}
	if got := p.requests(); string(got[len(got)-1].payload) != `{"n":3}` {
		t.Errorf("send-test pushed %q", got[len(got)-1].payload)
	}
	for _, bad := range [][]string{{"send-test", "--socket", r.sock}, {"send-test", "--socket", r.sock, "1000"}, {"send-test", "--socket", r.sock, "x"}, {"send-test", "3"}, {"send-test", "--socket", "n.sock", "3"}} {
		if code := dispatch(ctx, bad, &out, &errout); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}

	// A graceful stop: the code is 0 and the socket is gone.
	if code := r.stop(); code != 0 {
		t.Errorf("exit %d: %s", code, r.errout.text())
	}
	if _, err := os.Lstat(r.sock); !os.IsNotExist(err) {
		t.Error("the socket file was left")
	}
	// Nothing of an endpoint or a key in the log.
	text := r.errout.text()
	for _, bad := range []string{p.srv.URL, "/p/phone", testSubject, info.VAPIDPublic, s.P256DH, s.Auth} {
		if strings.Contains(text, bad) {
			t.Errorf("the log holds %q:\n%s", bad, text)
		}
	}
}

// TestRunNotifyWaitsForASlowPush: a push that the push service takes 2.5
// seconds over, longer than the 2 seconds of the other calls, is still
// accepted and counted through the socket, with the client's and the server's
// default deadlines for a notify.
func TestRunNotifyWaitsForASlowPush(t *testing.T) {
	p := newPush(t)
	r := start(t, p, uint32(os.Getuid()))
	c := r.client(t)
	ctx := context.Background()
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold["/p/slow"] = hold
	p.mu.Unlock()
	if err := c.Subscribe(ctx, p.subscription(t, "/p/slow")); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(2500*time.Millisecond, func() { close(hold) })
	begin := time.Now()
	sent, err := c.NotifySent(ctx, 2)
	if err != nil || sent != 1 {
		t.Fatalf("a push that took %v: sent %d: %v", time.Since(begin), sent, err)
	}
	if time.Since(begin) < 2*time.Second {
		t.Errorf("the push was held for only %v", time.Since(begin))
	}
}

// TestRunServesOnlyTheSeatUser: a peer that is not the seat user's id is
// closed unread, and the subscriptions it would have changed are not.
func TestRunServesOnlyTheSeatUser(t *testing.T) {
	p := newPush(t)
	r := start(t, p, uint32(os.Getuid())+1)
	c := r.client(t)
	if _, err := c.Info(context.Background()); err == nil {
		t.Fatal("a peer that is not the seat user was answered")
	}
	if err := c.Subscribe(context.Background(), p.subscription(t, "/p/x")); err == nil {
		t.Fatal("a peer that is not the seat user subscribed")
	}
	if _, err := os.Stat(r.subs); !os.IsNotExist(err) {
		t.Error("a subscription was written for the wrong peer")
	}
}

func TestRunKeepsSubscriptionsAcrossRestarts(t *testing.T) {
	p := newPush(t)
	dir := shortDir(t)
	keyPath, _ := newKey(t, dir)
	subs := filepath.Join(dir, "subscriptions.json")
	s := p.subscription(t, "/p/phone")
	st, err := openStore(subs, p.allow)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.put(s); err != nil {
		t.Fatal(err)
	}
	p.install(t, func(string) (uint32, error) { return uint32(os.Getuid()), nil })
	sock := filepath.Join(dir, "n.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{}, 1)
	done := make(chan int, 1)
	var errout syncBuffer
	go func() {
		done <- run(ctx, []string{"--socket", sock, "--seat-user", "seat", "--vapid-key", keyPath, "--subscriptions", subs,
			"--vapid-subject", testSubject}, &errout, func(net.Addr) { ready <- struct{}{} })
	}()
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("exit %d: %s", code, errout.text())
	}
	c, _ := seatnotice.NewClient(sock)
	if info, err := c.Info(context.Background()); err != nil || info.Subscriptions != 1 {
		t.Fatalf("info %+v: %v", info, err)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit %d", code)
	}
}

// TestRunRefusesToStart: an unknown seat user, a key or subscriptions file
// that is not safe, a stale-looking path that is not a socket, a second
// notifier on the socket: each is refused before anything is served, with a
// fixed text.
func TestRunRefusesToStart(t *testing.T) {
	p := newPush(t)
	dir := shortDir(t)
	keyPath, _ := newKey(t, dir)
	p.install(t, func(name string) (uint32, error) {
		if name == "seat" {
			return uint32(os.Getuid()), nil
		}
		return 0, user.UnknownUserError(name)
	})
	args := func(over map[string]string) []string {
		m := map[string]string{
			"--socket": filepath.Join(dir, "n.sock"), "--seat-user": "seat", "--vapid-key": keyPath,
			"--subscriptions": filepath.Join(dir, "subscriptions.json"), "--vapid-subject": testSubject,
		}
		for k, v := range over {
			m[k] = v
		}
		var out []string
		for k, v := range m {
			out = append(out, k, v)
		}
		return out
	}
	// A key file others can read.
	open := filepath.Join(dir, "open.pem")
	data, _ := os.ReadFile(keyPath)
	os.WriteFile(open, data, 0o644)
	// A subscriptions file with a wrong mode.
	loose := filepath.Join(dir, "loose.json")
	os.WriteFile(loose, []byte(goodJSON(p.subscription(t, "/p/1"))), 0o644)
	// A file where the socket should be.
	notSock := filepath.Join(dir, "plain")
	os.WriteFile(notSock, nil, 0o600)

	for name, c := range map[string]struct {
		over map[string]string
		code int
	}{
		"an unknown seat user":     {map[string]string{"--seat-user": "nobody-like-this"}, 2},
		"a key others can read":    {map[string]string{"--vapid-key": open}, 2},
		"no key":                   {map[string]string{"--vapid-key": filepath.Join(dir, "none.pem")}, 2},
		"a loose subscriptions":    {map[string]string{"--subscriptions": loose}, 2},
		"a path that is no socket": {map[string]string{"--socket": notSock}, 1},
	} {
		var errout syncBuffer
		code := run(context.Background(), args(c.over), &errout, func(net.Addr) { t.Errorf("%s: it served", name) })
		if code != c.code {
			t.Errorf("%s: exit %d, want %d: %s", name, code, c.code, errout.text())
		}
		if strings.Contains(errout.text(), dir) || strings.Contains(errout.text(), testSubject) {
			t.Errorf("%s: the diagnostic names a path or the subject:\n%s", name, errout.text())
		}
	}

	// A second notifier on the socket of a running one.
	r := start(t, p, uint32(os.Getuid()))
	var errout syncBuffer
	second := args(map[string]string{"--socket": r.sock, "--subscriptions": filepath.Join(dir, "second.json")})
	if code := run(context.Background(), second, &errout, nil); code != 1 {
		t.Errorf("a second notifier: exit %d", code)
	}
	if info, err := r.client(t).Info(context.Background()); err != nil || info.VAPIDPublic == "" {
		t.Errorf("the first notifier was disturbed: %v", err)
	}
}

// TestListenReplacesAStaleSocket: a socket file nothing answers on is
// replaced, and the new one has mode 0660 whatever the umask.
func TestListenReplacesAStaleSocket(t *testing.T) {
	dir := shortDir(t)
	stale := filepath.Join(dir, "n.sock")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Skip("no Unix sockets")
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Lstat(stale); err != nil {
		t.Fatal("no stale socket to replace")
	}
	old := syscall.Umask(0o077)
	lis, err := listen(stale)
	syscall.Umask(old)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	defer lis.Close()
	if fi, err := os.Lstat(stale); err != nil || fi.Mode().Perm() != 0o660 {
		t.Errorf("the new socket: %v %v", fi, err)
	}
}

// TestProcessStopsOnSIGTERM runs the command as a process: it serves, and a
// SIGTERM stops it with exit 0 and removes the socket.
func TestProcessStopsOnSIGTERM(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	if _, err := user.Lookup(u.Username); err != nil {
		t.Skip("the user database does not know this user")
	}
	dir := shortDir(t)
	keyPath, _ := newKey(t, dir)
	sock := filepath.Join(dir, "n.sock")
	cmd := exec.Command(os.Args[0], "run", "--socket", sock, "--seat-user", u.Username, "--vapid-key", keyPath,
		"--subscriptions", filepath.Join(dir, "subscriptions.json"), "--vapid-subject", testSubject)
	cmd.Env = append(os.Environ(), "ATANIQ_SEAT_NOTIFY_AS_MAIN=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			<-exited
		}
	}()
	c, _ := seatnotice.NewClient(sock)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.Info(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the process did not serve: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit: %v: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not stop on SIGTERM")
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Error("the socket file was left")
	}
	if !strings.Contains(stderr.String(), "stopped") {
		t.Errorf("stderr: %s", stderr.String())
	}
}

func TestDispatchRefusals(t *testing.T) {
	var out, errout bytes.Buffer
	for _, args := range [][]string{nil, {"serve"}, {"run"}, {"vapid-init"}} {
		out.Reset()
		errout.Reset()
		if code := dispatch(context.Background(), args, &out, &errout); code != 2 || out.Len() != 0 {
			t.Errorf("%v: exit %d, output %q", args, code, out.String())
		}
	}
}
