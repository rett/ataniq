//go:build linux && ataniq_canary

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadchat"
	"ataniq/internal/leadwire"
)

// chatRun runs "ataniqd chat ..." in a fresh app and returns its exit code,
// its output and its errors.
func chatRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	ta := newTestApp(t, nil)
	code := ta.run(append([]string{"chat"}, args...))
	return code, ta.out.String(), ta.err.String()
}

// The desk's chat command lists the leads' chat and sets a lead's switch on
// the daemon of the scratch config: every lead is off at start, read turns a
// defined lead's chat on, off turns it off; send, another switch, a key the
// daemon does not have, a lead whose chat cannot be used and a daemon that
// is not there each end with their own code.
func TestChatCommand(t *testing.T) {
	w := newScratchWorld(t)
	ta := scratchApp(t, w)
	socket := filepath.Join(w.Runtime, "daemon.sock")
	done := startApp(t, ta, []string{"--scratch", w.ConfigPath}, socket)

	code, out, errs := chatRun(t, "list", "--config", w.ConfigPath)
	if code != 0 {
		t.Fatalf("chat list: %d %s", code, errs)
	}
	var list leadwire.ChatListResult
	if err := json.Unmarshal([]byte(out), &list); err != nil || !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("chat list printed %q: %v", out, err)
	}
	if len(list.Leads) != 2 || list.Leads[0].Key != "alpha" || list.Leads[0].Chat != leadwire.ChatOff || list.Leads[1].Chat != leadwire.ChatOff {
		t.Fatalf("chat list: %+v", list)
	}

	// Not defined yet: the lead has no home, so read is unavailable.
	if code, _, errs := chatRun(t, "set", "alpha", "read", "--config", w.ConfigPath); code != 31 {
		t.Fatalf("read of a lead not defined: %d %s", code, errs)
	}
	ctx := context.Background()
	cl, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var r leadwire.Receipt
	if err := cl.Call(ctx, leadwire.OpDefine, leadwire.DefineParams{Client: "test", CommandID: "def-1", Role: "scratch-alpha/lead"}, &r); err != nil || r.Status != leadwire.StatusDone {
		t.Fatalf("define: %+v %v", r, err)
	}
	// The define made the reply folder.
	if fi, err := os.Lstat(filepath.Join(w.State, "leads", "scratch-alpha-lead", "home", leadchat.OutboxName)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the reply folder: %v %v", fi, err)
	}

	code, out, errs = chatRun(t, "set", "alpha", "read", "--config", w.ConfigPath)
	var row leadwire.ChatLead
	if code != 0 || json.Unmarshal([]byte(out), &row) != nil || row.Key != "alpha" || row.Chat != leadwire.ChatRead || row.Entries != 1 {
		t.Fatalf("set alpha read: %d %q %s", code, out, errs)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"set", "alpha", "send", "--config", w.ConfigPath}, 2},
		{[]string{"set", "alpha", "on", "--config", w.ConfigPath}, 2},
		{[]string{"set", "alpha", "--config", w.ConfigPath}, 2},
		{[]string{"set", "alpha", "read"}, 2},
		{[]string{"list"}, 2},
		{[]string{"list", "extra", "--config", w.ConfigPath}, 2},
		{[]string{"other", "--config", w.ConfigPath}, 2},
		{nil, 2},
		{[]string{"set", "gamma", "read", "--config", w.ConfigPath}, 30},
		{[]string{"list", "--config", filepath.Join(w.Dir, "missing.json")}, 10},
	} {
		if code, _, errs := chatRun(t, tc.args...); code != tc.code {
			t.Errorf("chat %q: %d, want %d: %s", tc.args, code, tc.code, errs)
		}
	}
	code, out, _ = chatRun(t, "set", "alpha", "off", "--config", w.ConfigPath)
	if code != 0 || json.Unmarshal([]byte(out), &row) != nil || row.Chat != leadwire.ChatOff {
		t.Fatalf("set alpha off: %d %q", code, out)
	}

	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	if code, _, _ := chatRun(t, "list", "--config", w.ConfigPath); code != 20 {
		t.Fatalf("chat list without a daemon: %d", code)
	}
}

// The usage names the chat command, its switch and that sending is not built.
func TestUsageNamesTheChatCommand(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	got := ta.out.String()
	for _, want := range []string{"ataniqd chat list --config CONFIG", "ataniqd chat set KEY off|read --config CONFIG", "Sending is not built", "off for every lead whenever the daemon starts"} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}
