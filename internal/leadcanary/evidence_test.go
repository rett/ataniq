//go:build linux && ataniq_canary

package leadcanary_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
)

const waiverWords = "assertion 10 waived: canary slice, no qualification record"

type evFixture struct {
	fixture
	cfg  leadcanary.Config
	ev   *leadcanary.Evidence
	path string
	tick int
}

func newEvidence(t *testing.T) *evFixture {
	t.Helper()
	e := &evFixture{fixture: newFixture(t)}
	e.cfg = e.config(t)
	e.path = filepath.Join(e.state, "canary-evidence.jsonl")
	clock := func() time.Time {
		e.tick++
		return time.Date(2026, 11, 17, 12, 0, e.tick, 0, time.FixedZone("offset", 3600))
	}
	ev, err := leadcanary.OpenEvidence(e.cfg, clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	e.ev = ev
	return e
}

func (e *evFixture) lines(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatalf("the file does not end in a newline: %q", data)
	}
	var out []map[string]any
	for _, l := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(l))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEvidenceFileModeAndName(t *testing.T) {
	e := newEvidence(t)
	var st unix.Stat_t
	if err := unix.Stat(e.path, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&07777 != 0o600 || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Geteuid() {
		t.Fatalf("mode %o uid %d", st.Mode, st.Uid)
	}
	if e.cfg.EvidencePath() != e.path || leadcanary.EvidenceName != "canary-evidence.jsonl" {
		t.Fatalf("path %q", e.cfg.EvidencePath())
	}
}

func TestEvidenceOpenedForAppend(t *testing.T) {
	e := newEvidence(t)
	var found bool
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	for _, d := range ents {
		link, err := os.Readlink(filepath.Join("/proc/self/fd", d.Name()))
		if err != nil || link != e.path {
			continue
		}
		info, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", d.Name()))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(info), "\n") {
			if rest, ok := strings.CutPrefix(l, "flags:"); ok {
				flags, err := strconv.ParseUint(strings.TrimSpace(rest), 8, 64)
				if err != nil {
					t.Fatal(err)
				}
				found = true
				if flags&unix.O_APPEND == 0 {
					t.Fatalf("flags %o: not O_APPEND", flags)
				}
				if flags&unix.O_ACCMODE != unix.O_WRONLY {
					t.Fatalf("flags %o: not write-only", flags)
				}
				if flags&unix.O_CLOEXEC == 0 {
					t.Fatalf("flags %o: not close-on-exec", flags)
				}
			}
		}
	}
	if !found {
		t.Fatal("no open handle to the evidence file")
	}
}

func TestEvidenceOneLinePerEventWithItsFields(t *testing.T) {
	e := newEvidence(t)
	appends := []leadcanary.Entry{
		{Event: leadcanary.EventDaemonStart},
		{Event: leadcanary.EventDefine},
		{Event: leadcanary.EventStart, Epoch: 1},
		{Event: leadcanary.EventTicket, Epoch: 1},
		{Event: leadcanary.EventInjectRefused, Epoch: 1, Code: "bad-id"},
		{Event: leadcanary.EventInjectSent, Epoch: 1, MessageID: "msg-1"},
		leadcanary.ResultEntry("msg-1", 1, leadcanary.NoAnswerResult()),
		{Event: leadcanary.EventStop, Epoch: 1},
	}
	for _, en := range appends {
		if err := e.ev.Append(en); err != nil {
			t.Fatalf("%+v: %v", en, err)
		}
	}
	lines := e.lines(t)
	if len(lines) != len(appends) {
		t.Fatalf("%d lines for %d events", len(lines), len(appends))
	}
	events := []string{"daemon-start", "define", "start", "ticket", "inject-refused", "inject-sent", "inject-result", "stop"}
	for i, l := range lines {
		if l["event"] != events[i] {
			t.Errorf("line %d: event %v, want %s", i, l["event"], events[i])
		}
		if l["role"] != "throwaway-one/lead" || l["agent"] != "stand-in" {
			t.Errorf("line %d: role %v agent %v", i, l["role"], l["agent"])
		}
		at, ok := l["at"].(string)
		if !ok {
			t.Fatalf("line %d: no at", i)
		}
		ts, err := time.Parse(time.RFC3339Nano, at)
		if err != nil || !strings.HasSuffix(at, "Z") {
			t.Errorf("line %d: at %q is not RFC 3339 in UTC (%v)", i, at, err)
		}
		if ts.Hour() != 11 { // the clock's offset of one hour is converted
			t.Errorf("line %d: at %q was not converted to UTC", i, at)
		}
		hasQual := events[i] == "define" || events[i] == "start"
		if _, has := l["qualification"]; has != hasQual {
			t.Errorf("line %d (%s): qualification present %v", i, events[i], has)
		}
		if _, has := l["login"]; has != hasQual {
			t.Errorf("line %d (%s): login present %v", i, events[i], has)
		}
		if hasQual && (l["qualification"] != "none (canary waiver)" || l["login"] != "not checked") {
			t.Errorf("line %d: qualification %v login %v", i, l["qualification"], l["login"])
		}
		_, hasWaiver := l["waiver"]
		if hasWaiver != (events[i] == "start") {
			t.Errorf("line %d (%s): waiver present %v", i, events[i], hasWaiver)
		}
		if events[i] == "start" && l["waiver"] != waiverWords {
			t.Errorf("waiver %v", l["waiver"])
		}
	}
	// Times are taken per event and increase.
	if lines[0]["at"] == lines[1]["at"] {
		t.Error("two events have the same time")
	}
	// Epoch and message fields.
	if _, has := lines[0]["epoch"]; has {
		t.Error("the daemon-start line has an epoch")
	}
	if n, _ := lines[2]["epoch"].(json.Number); n.String() != "1" {
		t.Errorf("start epoch %v", lines[2]["epoch"])
	}
	if lines[4]["code"] != "bad-id" {
		t.Errorf("refused line: %v", lines[4])
	}
	if _, has := lines[4]["message_id"]; has {
		t.Error("the refused line has a message id that nobody gave")
	}
	if lines[5]["message_id"] != "msg-1" {
		t.Errorf("sent line: %v", lines[5])
	}
	res := lines[6]
	if res["message_id"] != "msg-1" || res["outcome"] != "uncertain" || res["reason"] != "died" ||
		res["written"] != "unknown" || res["detail"] != "no answer from the holder" {
		t.Errorf("result line: %v", res)
	}
}

func TestEvidenceKeysAreOnlyTheListedOnes(t *testing.T) {
	e := newEvidence(t)
	if err := e.ev.Append(leadcanary.ResultEntry("msg-1", 1, validResults[5])); err != nil {
		t.Fatal(err)
	}
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventStart, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("at event role epoch message_id outcome reason written detail waiver agent code qualification login") {
		allowed[k] = true
	}
	for _, l := range e.lines(t) {
		for k := range l {
			if !allowed[k] {
				t.Errorf("unexpected key %q", k)
			}
		}
	}
}

func TestEvidenceAgentKindGoesIntoEveryLine(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	cfg.Agent.Kind = leadcanary.AgentLive
	cfg.Agent.FooterMode = "manual"
	cfg.Agent.Argv = []string{"/ataniq/agent/claude"}
	ev, err := leadcanary.OpenEvidence(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	if err := ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg.EvidencePath())
	if !strings.Contains(string(data), `"agent":"live"`) || strings.Contains(string(data), "stand-in") {
		t.Fatalf("line %s", data)
	}
	// The default clock gives a plausible UTC time.
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &m); err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339Nano, m["at"].(string))
	if err != nil || time.Since(ts) > time.Minute || time.Since(ts) < -time.Minute {
		t.Fatalf("at %v (%v)", m["at"], err)
	}
}

func TestEvidenceAppendsAcrossOpens(t *testing.T) {
	e := newEvidence(t)
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
		t.Fatal(err)
	}
	if err := e.ev.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.ev.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err == nil {
		t.Fatal("appended after Close")
	}
	ev2, err := leadcanary.OpenEvidence(e.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ev2.Close()
	if err := ev2.Append(leadcanary.Entry{Event: leadcanary.EventDefine}); err != nil {
		t.Fatal(err)
	}
	lines := e.lines(t)
	if len(lines) != 2 || lines[0]["event"] != "daemon-start" || lines[1]["event"] != "define" {
		t.Fatalf("lines %v", lines)
	}
}

func TestEvidenceRefusesAFileThatIsNotOurs(t *testing.T) {
	t.Run("wrong mode", func(t *testing.T) {
		f := newFixture(t)
		writeFile(t, f.config(t).EvidencePath(), nil, 0o644)
		if _, err := leadcanary.OpenEvidence(f.config(t), nil); err == nil {
			t.Fatal("opened a file with mode 0644")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		f := newFixture(t)
		target := filepath.Join(f.dir, "elsewhere.jsonl")
		writeFile(t, target, nil, 0o600)
		if err := os.Symlink(target, f.config(t).EvidencePath()); err != nil {
			t.Fatal(err)
		}
		if _, err := leadcanary.OpenEvidence(f.config(t), nil); err == nil {
			t.Fatal("followed a symbolic link")
		}
		if st, _ := os.Stat(target); st.Size() != 0 {
			t.Fatal("wrote through a symbolic link")
		}
	})
	t.Run("fifo", func(t *testing.T) {
		f := newFixture(t)
		if err := unix.Mkfifo(f.config(t).EvidencePath(), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := leadcanary.OpenEvidence(f.config(t), nil); err == nil {
			t.Fatal("opened a named pipe")
		}
	})
	t.Run("directory", func(t *testing.T) {
		f := newFixture(t)
		if err := os.Mkdir(f.config(t).EvidencePath(), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := leadcanary.OpenEvidence(f.config(t), nil); err == nil {
			t.Fatal("opened a directory")
		}
	})
	t.Run("state root missing", func(t *testing.T) {
		f := newFixture(t)
		cfg := f.config(t)
		cfg.StateRoot = filepath.Join(f.dir, "nothing-here")
		if _, err := leadcanary.OpenEvidence(cfg, nil); err == nil {
			t.Fatal("opened under a missing state root")
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		f := newFixture(t)
		cfg := f.config(t)
		cfg.Limits = leadcanary.Limits{}
		if _, err := leadcanary.OpenEvidence(cfg, nil); err == nil {
			t.Fatal("opened for a config that is not valid")
		}
	})
	t.Run("a mode set by the umask is put right", func(t *testing.T) {
		f := newFixture(t)
		old := unix.Umask(0o277)
		defer unix.Umask(old)
		ev, err := leadcanary.OpenEvidence(f.config(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ev.Close()
		var st unix.Stat_t
		if err := unix.Stat(f.config(t).EvidencePath(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&07777 != 0o600 {
			t.Fatalf("mode %o", st.Mode&07777)
		}
		if err := ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEvidenceRefusesAnInvalidEntryAndWritesNothing(t *testing.T) {
	e := newEvidence(t)
	cases := map[string]leadcanary.Entry{
		"no event":             {},
		"unknown event":        {Event: "other"},
		"bad message id":       {Event: leadcanary.EventInjectSent, Epoch: 1, MessageID: "Bad Id"},
		"inject-sent no id":    {Event: leadcanary.EventInjectSent, Epoch: 1},
		"inject-sent no epoch": {Event: leadcanary.EventInjectSent, MessageID: "m"},
		"start no epoch":       {Event: leadcanary.EventStart},
		"ticket no epoch":      {Event: leadcanary.EventTicket},
		"result no outcome":    {Event: leadcanary.EventInjectResult, Epoch: 1, MessageID: "m", Written: "whole"},
		"result no written":    {Event: leadcanary.EventInjectResult, Epoch: 1, MessageID: "m", Outcome: "submitted"},
		"result no id":         {Event: leadcanary.EventInjectResult, Epoch: 1, Outcome: "submitted", Written: "whole"},
		"refused no code":      {Event: leadcanary.EventInjectRefused},
		"code with space":      {Event: leadcanary.EventInjectRefused, Code: "bad code"},
		"code with upper case": {Event: leadcanary.EventInjectRefused, Code: "Bad"},
		"code over 64":         {Event: leadcanary.EventInjectRefused, Code: strings.Repeat("a", 65)},
		"outcome with newline": {Event: leadcanary.EventInjectResult, Epoch: 1, MessageID: "m", Outcome: "a\nb", Written: "whole"},
		"detail with control":  {Event: leadcanary.EventStop, Detail: "a\x1b[0m"},
		"detail non-ASCII":     {Event: leadcanary.EventStop, Detail: "caf\u00e9"},
		"detail over 512":      {Event: leadcanary.EventStop, Detail: strings.Repeat("a", 513)},
	}
	for name, en := range cases {
		err := e.ev.Append(en)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !errors.Is(err, leadcanary.ErrEntry) {
			t.Errorf("%s: %v does not wrap ErrEntry", name, err)
		}
	}
	if st, err := os.Stat(e.path); err != nil || st.Size() != 0 {
		t.Fatalf("a refused entry wrote something: %v %v", st, err)
	}
	// A detail of exactly 512 bytes is fine.
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventStop, Detail: strings.Repeat("a", 512)}); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceNeverHoldsTheMessageBody(t *testing.T) {
	e := newEvidence(t)
	const marker = "body-marker-7f3a9c"
	adm := newAdmission(t)
	body := "canary-token: tok-" + marker + "\nmore text " + marker
	text, err := adm.Admit("msg-1", body)
	if err != nil {
		t.Fatal(err)
	}
	// What a daemon writes around one message.
	steps := []leadcanary.Entry{
		{Event: leadcanary.EventInjectSent, Epoch: 1, MessageID: "msg-1"},
		leadcanary.ResultEntry("msg-1", 1, validResults[5]),
		leadcanary.ResultEntry("msg-1", 1, leadcanary.NoAnswerResult()),
	}
	for _, s := range steps {
		if err := e.ev.Append(s); err != nil {
			t.Fatal(err)
		}
	}
	// Entry has no field for the body: every string field is bounded to a
	// token, an id or printable detail, and the body does not appear.
	data, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{marker, "canary-token", "more text", text, strings.SplitN(text, "\n", 2)[0]} {
		if bytes.Contains(data, []byte(needle)) {
			t.Fatalf("the evidence holds %q:\n%s", needle, data)
		}
	}
}

func TestEvidenceConcurrentAppendsStayWholeLines(t *testing.T) {
	e := newEvidence(t)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventTicket, Epoch: uint64(i + 1)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	lines := e.lines(t)
	if len(lines) != n {
		t.Fatalf("%d lines, want %d", len(lines), n)
	}
	seen := map[string]bool{}
	var last time.Time
	for _, l := range lines {
		seen[fmt.Sprint(l["epoch"])] = true
		// The test clock moves one second a call, so the times in the file are
		// in order only when each is taken while the line is written.
		at, err := time.Parse(time.RFC3339Nano, l["at"].(string))
		if err != nil || !at.After(last) {
			t.Fatalf("time %v after %v (%v)", l["at"], last, err)
		}
		last = at
	}
	if len(seen) != n {
		t.Fatalf("%d distinct epochs, want %d", len(seen), n)
	}
}

func TestEvidenceEventNames(t *testing.T) {
	for ev, want := range map[leadcanary.Event]string{
		leadcanary.EventDaemonStart:   "daemon-start",
		leadcanary.EventDefine:        "define",
		leadcanary.EventStart:         "start",
		leadcanary.EventTicket:        "ticket",
		leadcanary.EventInjectRefused: "inject-refused",
		leadcanary.EventInjectSent:    "inject-sent",
		leadcanary.EventInjectResult:  "inject-result",
		leadcanary.EventStop:          "stop",
	} {
		if string(ev) != want {
			t.Errorf("event %q, want %q", ev, want)
		}
	}
}

// TestEvidenceAcceptsEveryAdmitCode shows that a refusal code of the admission
// is a token the evidence takes in its code field.
func TestEvidenceAcceptsEveryAdmitCode(t *testing.T) {
	e := newEvidence(t)
	for _, c := range allAdmitCodes {
		if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventInjectRefused, Code: string(c)}); err != nil {
			t.Fatalf("code %q is not an evidence token: %v", c, err)
		}
	}
	if got := len(e.lines(t)); got != len(allAdmitCodes) {
		t.Fatalf("%d lines for %d codes", got, len(allAdmitCodes))
	}
}
