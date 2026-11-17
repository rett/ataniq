package leaddaemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ataniq/internal/clientapi"
)

// stubHandler answers with what the test chooses.
type stubHandler struct {
	version string
	status  clientapi.StatusResult
	err     error
}

func (h *stubHandler) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: h.version}
}

func (h *stubHandler) Status(context.Context) (clientapi.StatusResult, error) {
	return h.status, h.err
}

// serveStub serves h on a private socket and returns the socket path.
func serveStub(t *testing.T, h clientapi.Handler, opts clientapi.Options) string {
	t.Helper()
	e := newEnv(t)
	l, path, err := clientapi.Listen(e.runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { clientapi.Serve(ctx, l, h, opts); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return path
}

func runStatus(t *testing.T, opts StatusOptions) (code int, stdout, stderr string) {
	t.Helper()
	var out, errw bytes.Buffer
	code = RunStatus(context.Background(), opts, &out, &errw)
	return code, out.String(), errw.String()
}

func sampleStatus() clientapi.StatusResult {
	at := t0.Add(time.Minute)
	one, two := uint64(1), uint64(2)
	return clientapi.StatusResult{
		Daemon: clientapi.DaemonStatus{PID: 4242, StartedAt: t0, RegistryRevision: 17, BootID: "boot-1",
			LastReconcileCompleted: true, LastReconcileAt: &at},
		Leads: []clientapi.LeadStatus{
			{Role: "alpha/lead", Kind: "lead", Project: "alpha", Manager: "ataniq", Epoch: 3, Desired: "running",
				State: "ready", ReasonCode: "test-setup", Since: t0},
			{Role: "alpha/coord/feat", Kind: "coordinator", Project: "alpha", Manager: "ataniq", Epoch: 2, Desired: "running",
				State: "stalled", ReasonCode: "no-progress", ReasonDetail: "quiet for 10 min", Since: t0,
				Feature: "feat", Parent: "alpha/lead", ParentEpoch: &one, Instance: &two, Identity: "alpha-lead@e1:coord:feat:2"},
			{Role: "supervisor", Kind: "supervisor", Manager: "ataniq", Desired: "stopped", State: "stopped", ReasonCode: "defined", Since: t0},
		},
	}
}

func TestStatusCommandHumanOutput(t *testing.T) {
	path := serveStub(t, &stubHandler{version: "1.2.3", status: sampleStatus()}, clientapi.Options{})
	code, out, errText := runStatus(t, StatusOptions{Socket: path})
	if code != ExitOK || errText != "" {
		t.Fatalf("code %d, stderr %q", code, errText)
	}
	want := `daemon version=1.2.3 pid=4242 started=2026-03-01T10:00:00Z revision=17 boot=boot-1 reconcile=completed reconcile_at=2026-03-01T10:01:00Z
role=alpha/lead kind=lead state=ready reason=test-setup epoch=3 desired=running manager=ataniq
role=alpha/coord/feat kind=coordinator state=stalled reason=no-progress epoch=2 desired=running manager=ataniq parent=alpha/lead instance=2 detail="quiet for 10 min"
role=supervisor kind=supervisor state=stopped reason=defined epoch=0 desired=stopped manager=ataniq
`
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestStatusCommandIncompleteReconcile(t *testing.T) {
	st := sampleStatus()
	st.Daemon.LastReconcileCompleted = false
	st.Daemon.LastReconcileAt = nil
	path := serveStub(t, &stubHandler{version: "v", status: st}, clientapi.Options{})
	code, out, _ := runStatus(t, StatusOptions{Socket: path})
	if code != ExitOK || !strings.Contains(out, "reconcile=incomplete reconcile_at=never") {
		t.Fatalf("code %d, output:\n%s", code, out)
	}
}

func TestStatusCommandJSON(t *testing.T) {
	want := sampleStatus()
	path := serveStub(t, &stubHandler{version: "v", status: want}, clientapi.Options{})
	code, out, errText := runStatus(t, StatusOptions{Socket: path, JSON: true})
	if code != ExitOK || errText != "" {
		t.Fatalf("code %d, stderr %q", code, errText)
	}
	var got clientapi.StatusResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, out)
	}
	if got.Daemon.PID != 4242 || len(got.Leads) != 3 || got.Leads[1].Identity != "alpha-lead@e1:coord:feat:2" ||
		got.Leads[1].Instance == nil || *got.Leads[1].Instance != 2 || got.Daemon.LastReconcileAt == nil {
		t.Fatalf("decoded %+v", got)
	}
	// A lead has no coordinator fields on the wire.
	var raw struct{ Leads []map[string]json.RawMessage }
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw.Leads[0]["feature"]; has {
		t.Fatal("a lead has a feature member")
	}
}

// hostile is a string that, printed raw, would act on a terminal.
const hostile = "evil\x1b]0;owned\x07\x1b[2J\r\nrole=fake state=ready\x00\x7f\u009b31m\u202e\u2028\U0001F4A5\xff\"\\"

func hostileStatus() clientapi.StatusResult {
	st := sampleStatus()
	st.Daemon.BootID = hostile
	st.Leads[0].Role = hostile
	st.Leads[0].Kind = hostile
	st.Leads[0].State = hostile
	st.Leads[0].ReasonCode = hostile
	st.Leads[0].ReasonDetail = hostile
	st.Leads[0].Desired = hostile
	st.Leads[0].Manager = hostile
	st.Leads[1].Parent = hostile
	st.Leads[1].Feature = hostile
	st.Leads[1].Identity = hostile
	return st
}

// terminalSafe reports why out could act on a terminal, or "".
func terminalSafe(out string) string {
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c == '\n' {
			continue
		}
		if c < 0x20 || c >= 0x7f {
			return "byte " + string(rune('0'+c/100)) + string(rune('0'+c/10%10)) + string(rune('0'+c%10))
		}
	}
	return ""
}

func TestStatusCommandEscapesHostileStrings(t *testing.T) {
	// The hostile text goes through JSON on the wire, which is valid for the
	// strings with a control character but not for the invalid byte 0xff:
	// encoding/json turns that into U+FFFD, so the client sees that.
	path := serveStub(t, &stubHandler{version: hostile, status: hostileStatus()}, clientapi.Options{})
	code, out, errText := runStatus(t, StatusOptions{Socket: path})
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errText)
	}
	if why := terminalSafe(out); why != "" {
		t.Fatalf("the output can act on a terminal (%s): %q", why, out)
	}
	if lines := strings.Count(out, "\n"); lines != 4 {
		t.Fatalf("%d lines: a string split a line:\n%q", lines, out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "role=fake") && !strings.Contains(out, `\r\nrole=fake`) {
		t.Fatalf("unescaped: %q", out)
	}
	for _, want := range []string{`\x1b]0;owned\x07`, `\r\n`, `\x00`, `\x7f`, `\u009b`, `\u202e`, `\u2028`, `\U0001f4a5`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks the escape %s:\n%s", want, out)
		}
	}
}

func TestStatusCommandJSONEscapesHostileStrings(t *testing.T) {
	st := hostileStatus()
	path := serveStub(t, &stubHandler{version: "v", status: st}, clientapi.Options{})
	code, out, errText := runStatus(t, StatusOptions{Socket: path, JSON: true})
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errText)
	}
	if why := terminalSafe(out); why != "" {
		t.Fatalf("the JSON can act on a terminal (%s): %q", why, out)
	}
	var got clientapi.StatusResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// The wire carried valid UTF-8 only, so what arrives decodes to what the
	// server sent, 0xff apart.
	wantRole := strings.ToValidUTF8(hostile, "\ufffd")
	if got.Leads[0].Role != wantRole || got.Daemon.BootID != wantRole {
		t.Fatalf("round trip: %q, want %q", got.Leads[0].Role, wantRole)
	}
}

// TestAsciiJSONWritesOnlyASCII: whatever a string holds, also bytes that are
// not valid UTF-8 and the replacement character itself, every byte of the
// output is printable ASCII or layout, and the text decodes to the same value.
func TestAsciiJSONWritesOnlyASCII(t *testing.T) {
	for _, v := range []string{"a\xffb", "\ufffd", "\x7f", "\u00e9\U0001f600"} {
		out, err := asciiJSON(map[string]string{"k": v})
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range out {
			if b >= 0x7f || (b < 0x20 && b != '\n') {
				t.Fatalf("%q: byte %#x in %q", v, b, out)
			}
		}
		var got map[string]string
		if err := json.Unmarshal(out, &got); err != nil || got["k"] != strings.ToValidUTF8(v, "\ufffd") {
			t.Fatalf("%q: decodes to %q, %v", v, got["k"], err)
		}
	}
}

func TestSafeText(t *testing.T) {
	for in, want := range map[string]string{
		"plain text-1":  "plain text-1",
		"a\\b":          `a\\b`,
		"tab\there\n":   `tab\there\n`,
		"\x1b[31m":      `\x1b[31m`,
		"\xc3\x28":      `\xc3(`,
		"caf\u00e9":     `caf\u00e9`,
		"\u0085":        `\u0085`,
		"\U0010ffff":    `\U0010ffff`,
		"":              "",
		"\x7f":          `\x7f`,
		"quote\"inside": `quote"inside`,
	} {
		if got := SafeText(in); got != want {
			t.Errorf("SafeText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := quote(`a"b`); got != `"a\"b"` {
		t.Errorf("quote: %s", got)
	}
}

func TestStatusCommandNoDaemon(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Run("socket missing", func(t *testing.T) {
		code, out, errText := runStatus(t, StatusOptions{Socket: e.socket()})
		if code != ExitNoDaemon || out != "" || !strings.Contains(errText, "no daemon") {
			t.Fatalf("code %d, out %q, err %q", code, out, errText)
		}
	})
	t.Run("folder missing", func(t *testing.T) {
		code, _, _ := runStatus(t, StatusOptions{Socket: filepath.Join(e.base, "nope", "daemon.sock")})
		if code != ExitNoDaemon {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("stale socket, connection refused", func(t *testing.T) {
		makeStaleSocket(t, e.socket())
		code, _, _ := runStatus(t, StatusOptions{Socket: e.socket()})
		if code != ExitNoDaemon {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("a file where the socket would be", func(t *testing.T) {
		p := filepath.Join(e.runtime, "file")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, _ := runStatus(t, StatusOptions{Socket: p})
		if code != ExitNoDaemon {
			t.Fatalf("code %d", code)
		}
	})
}

func TestStatusCommandUnauthorized(t *testing.T) {
	path := serveStub(t, &stubHandler{status: sampleStatus()}, clientapi.Options{
		PeerUID: func(net.Conn) (uint32, error) { return uint32(os.Geteuid()) + 1, nil },
	})
	for _, json := range []bool{false, true} {
		code, out, errText := runStatus(t, StatusOptions{Socket: path, JSON: json})
		if code != ExitUnauthorized || out != "" || !strings.Contains(errText, "unauthorized") {
			t.Fatalf("json=%v: code %d, out %q, err %q", json, code, out, errText)
		}
	}
}

// rawServer accepts connections on a private socket and hands each to fn.
func rawServer(t *testing.T, fn func(c net.Conn)) string {
	t.Helper()
	e := newEnv(t)
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", e.socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); fn(c) }()
		}
	}()
	return e.socket()
}

func TestStatusCommandProtocolErrors(t *testing.T) {
	reply := func(line string) func(net.Conn) {
		return func(c net.Conn) {
			bufio.NewReader(c).ReadString('\n')
			c.Write([]byte(line))
		}
	}
	for name, fn := range map[string]func(net.Conn){
		"not JSON":       reply("this is not json\n"),
		"wrong protocol": reply(`{"protocol":"other/9","request_id":"c-1","ok":true,"result":{}}` + "\n"),
		"wrong id":       reply(`{"protocol":"ataniq.client/1","request_id":"x","ok":true,"result":{}}` + "\n"),
		"closed at once": func(c net.Conn) {},
		"too long a line": func(c net.Conn) {
			bufio.NewReader(c).ReadString('\n')
			c.Write(append(bytes.Repeat([]byte("a"), clientapi.MaxResponseBytes+10), '\n'))
		},
		"an error answer": reply(`{"protocol":"ataniq.client/1","request_id":"c-1","ok":false,"error":{"code":"internal\u001b[2J","message":"m"}}` + "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			path := rawServer(t, fn)
			code, out, errText := runStatus(t, StatusOptions{Socket: path})
			if code != ExitProtocol || out != "" {
				t.Fatalf("code %d, out %q, err %q", code, out, errText)
			}
			if why := terminalSafe(errText); why != "" {
				t.Fatalf("the message can act on a terminal (%s): %q", why, errText)
			}
			if strings.Count(errText, "\n") != 1 {
				t.Fatalf("not one line: %q", errText)
			}
		})
	}
}

func TestStatusCommandServerErrorIsAProtocolError(t *testing.T) {
	path := serveStub(t, &stubHandler{err: errors.New("registry failed")}, clientapi.Options{})
	code, out, errText := runStatus(t, StatusOptions{Socket: path})
	if code != ExitProtocol || out != "" || !strings.Contains(errText, "internal") {
		t.Fatalf("code %d, out %q, err %q", code, out, errText)
	}
}

func TestStatusCommandTimeout(t *testing.T) {
	path := rawServer(t, func(c net.Conn) { bufio.NewReader(c).ReadString('\n'); time.Sleep(2 * time.Second) })
	began := time.Now()
	code, _, errText := runStatus(t, StatusOptions{Socket: path, Timeout: 150 * time.Millisecond})
	if code != ExitFailure || time.Since(began) > time.Second || !strings.Contains(errText, "in time") {
		t.Fatalf("code %d after %s, err %q", code, time.Since(began), errText)
	}
}

func TestStatusCommandAgainstTheDaemon(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	d := e.startDaemon(e.config())
	defer d.stop()
	code, out, errText := runStatus(t, StatusOptions{Socket: d.socket})
	if code != ExitOK || errText != "" {
		t.Fatalf("code %d, err %q", code, errText)
	}
	if !strings.HasPrefix(out, "daemon version=9.9.9-test pid=") || !strings.Contains(out, "reconcile=completed") ||
		!strings.Contains(out, "role=alpha/coord/feat kind=coordinator state=ready") || !strings.Contains(out, "parent=alpha/lead instance=1") ||
		strings.Count(out, "\n") != 6 || !utf8.ValidString(out) {
		t.Fatalf("output:\n%s", out)
	}
}
