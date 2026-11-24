//go:build linux && ataniq_ownerchannel

package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/handoff"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/leaddaemon"
)

// The ids the test store gives its threads, in the order of the starts: two
// that share their first 6 digits, one of another kind, and one left open.
const (
	idApp   = "a1b2c3d4e5f60001"
	idTool  = "a1b2c3ffffff0002"
	idPub   = "0f0e0d0c0b0a0909"
	idOpen  = "1234567890abcdef"
	idLater = "fedcba9876543210"
)

func idBytes(t *testing.T, ids ...string) []byte {
	t.Helper()
	var out []byte
	for _, id := range ids {
		for i := 0; i < len(id); i += 2 {
			var b byte
			for _, c := range id[i : i+2] {
				b <<= 4
				switch {
				case c >= '0' && c <= '9':
					b |= byte(c - '0')
				default:
					b |= byte(c-'a') + 10
				}
			}
			out = append(out, b)
		}
	}
	return out
}

// interview walks one interview through the store's verbs, answering each
// question from picks by node id, else skipping it when it has a default,
// else with its first option; and finishes it when finish says so.
func interview(t *testing.T, s *thread.Store, picks map[string]string, finish bool) string {
	t.Helper()
	ctx := context.Background()
	res, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Thread
	for i := 0; res.State == ownerwire.GenesisStateQuestion; i++ {
		if i > 20 {
			t.Fatal("the interview does not end")
		}
		var answers []ownerwire.GenesisAnswer
		for _, n := range res.Nodes {
			a := ownerwire.GenesisAnswer{Node: n.ID}
			switch pick, ok := picks[n.ID]; {
			case ok:
				a.Kind, a.IDs = ownerwire.GenesisKindChoice, []string{pick}
			case n.CanSkip:
				a.Kind = ownerwire.GenesisKindSkip
			case n.Type == "multi":
				a.Kind, a.IDs = ownerwire.GenesisKindChoices, []string{n.Options[0].ID}
			default:
				a.Kind, a.IDs = ownerwire.GenesisKindChoice, []string{n.Options[0].ID}
			}
			answers = append(answers, a)
		}
		if res, err = s.Answer(ctx, id, res.Round, answers); err != nil {
			t.Fatalf("round %d: %v", res.Round, err)
		}
	}
	if finish {
		if _, err := s.Finish(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// genesisState makes a state folder whose owner/genesis holds, written by the
// real thread store: a finished web app, a finished one-page tool, a finished
// newsletter and an open interview. It returns the state folder and the
// store, still open, as a running daemon holds it.
func genesisState(t *testing.T) (string, *thread.Store) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(state, "owner"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := thread.Open(genesisDir(state), thread.Options{Rand: bytes.NewReader(idBytes(t, idApp, idTool, idPub, idOpen, idLater))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, c := range []struct {
		want  string
		picks map[string]string
	}{
		{idApp, map[string]string{"kind": "software", "software.sub_kind": "web_app"}},
		{idTool, map[string]string{"kind": "software", "software.sub_kind": "web_tool"}},
		{idPub, map[string]string{"kind": "publishing", "publishing.sub_kind": "newsletter"}},
	} {
		if id := interview(t, s, c.picks, true); id != c.want {
			t.Fatalf("the thread %s, want %s", id, c.want)
		}
	}
	if id := interview(t, s, map[string]string{"kind": "software", "software.sub_kind": "web_app"}, false); id != idOpen {
		t.Fatalf("the open thread %s", id)
	}
	return state, s
}

// fileState is what a write would change of a file or folder.
type fileState struct {
	ino   uint64
	mode  os.FileMode
	size  int64
	mtime syscall.Timespec
	ctime syscall.Timespec
	data  string
}

// tree takes the state of every file and folder under root.
func tree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := fi.Sys().(*syscall.Stat_t)
		s := fileState{ino: st.Ino, mode: fi.Mode(), size: fi.Size(), mtime: st.Mtim, ctime: st.Ctim}
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s.data = string(b)
		}
		out[p] = s
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, before, after map[string]fileState) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("the state folder had %d entries and has %d", len(before), len(after))
	}
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			t.Fatalf("%s changed", p)
		}
	}
}

var listLine = regexp.MustCompile(`^([0-9a-f]{6})  (software|publishing|media)  ([0-9a-f]{12})  (\d{4}-\d{2}-\d{2})$`)

// genesis list prints the finished interviews with a plan, one line each, and
// genesis handoff prints the hand-off of one by its tag, or refuses with exit
// code 3; both read the folder with the store open on it, and nothing in the
// state folder changes.
func TestGenesisListAndHandoff(t *testing.T) {
	state, _ := genesisState(t)
	sums, warnings, err := thread.ReadFinished(genesisDir(state))
	if err != nil || len(warnings) != 0 || len(sums) != 3 {
		t.Fatalf("the reader: %+v %q %v", sums, warnings, err)
	}
	before := tree(t, state)

	ta := newTestApp(t, nil)
	if code := ta.run([]string{"genesis", "list", "--state-dir", state}); code != 0 || ta.err.Len() != 0 {
		t.Fatalf("list: exit %d: %q", code, ta.err.String())
	}
	lines := strings.Split(strings.TrimSuffix(ta.out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("list printed %q", ta.out.String())
	}
	for i, l := range lines {
		m := listLine.FindStringSubmatch(l)
		s := sums[i]
		if m == nil || m[1] != s.Tag || m[2] != s.Kind || m[3] != s.PlanShort || m[4] != s.Finished.Format("2006-01-02") {
			t.Errorf("line %q, summary %+v", l, s)
		}
		if strings.HasPrefix(l, idOpen[:6]) {
			t.Errorf("the open thread is listed: %q", l)
		}
	}

	for id, name := range map[string]string{idApp: "webapp" + idApp[:6], idTool: "webtool" + idTool[:6]} {
		for _, tag := range []string{id, id[:7]} {
			ta := newTestApp(t, nil)
			if code := ta.run([]string{"genesis", "handoff", tag, "--state-dir", state}); code != 0 || ta.err.Len() != 0 {
				t.Fatalf("handoff %s: exit %d: %q", tag, code, ta.err.String())
			}
			p, got, err := thread.ReadPlan(genesisDir(state), id)
			if err != nil || got != id {
				t.Fatal(err)
			}
			h, err := handoff.Emit(p, id)
			if err != nil {
				t.Fatal(err)
			}
			if ta.out.String() != h.Text() {
				t.Fatalf("handoff %s printed\n%s\nwant\n%s", tag, ta.out.String(), h.Text())
			}
			out := strings.TrimSuffix(ta.out.String(), "\n")
			last := out[strings.LastIndexByte(out, '\n')+1:]
			if last != "qargi project new "+name+" --methodology gitflow" || handoff.ValidateCommand(last) != nil {
				t.Fatalf("the command is %q", last)
			}
		}
	}

	for _, c := range []struct {
		tag, text string
	}{
		{idPub[:6], handoff.TextNotAvailable},
		{idApp[:6], "more than one finished interview with a plan has that tag"},
		{idOpen[:6], "no finished interview with a plan has that tag"},
		{"ffffff", "no finished interview with a plan has that tag"},
		{"abc", "a tag is 6 to 16 lower-case hexadecimal digits"},
		{"A1B2C3", "a tag is 6 to 16 lower-case hexadecimal digits"},
	} {
		ta := newTestApp(t, nil)
		code := ta.run([]string{"genesis", "handoff", c.tag, "--state-dir", state})
		if code != 3 || ta.out.Len() != 0 || !strings.Contains(ta.err.String(), c.text) {
			t.Errorf("handoff %s: exit %d: %q %q", c.tag, code, ta.out.String(), ta.err.String())
		}
	}
	sameTree(t, before, tree(t, state))
}

// The commands work with no daemon (the store closed) and from the default
// state folder; a state folder with no interview folder lists none and is not
// given one; a file the store would not serve is named on standard error and
// the rest is listed.
func TestGenesisWithoutTheDaemon(t *testing.T) {
	state, s := genesisState(t)
	s.Close()
	base := filepath.Dir(state)
	if err := os.Rename(state, filepath.Join(base, "ataniq")); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(base, "ataniq")
	env := map[string]string{"XDG_STATE_HOME": base}
	if err := os.WriteFile(filepath.Join(genesisDir(state), "9999999999999999.json"), []byte(`{"text":"HOSTILE"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, state)
	ta := newTestApp(t, env)
	if code := ta.run([]string{"genesis", "list"}); code != 0 || strings.Count(ta.out.String(), "\n") != 3 ||
		!strings.Contains(ta.err.String(), "the thread file 9999999999999999.json is not served") || strings.Contains(ta.err.String(), "HOSTILE") {
		t.Fatalf("list: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}
	ta = newTestApp(t, env)
	if code := ta.run([]string{"genesis", "handoff", idTool[:7]}); code != 0 || !strings.HasSuffix(ta.out.String(), "qargi project new webtool"+idTool[:6]+" --methodology gitflow\n") {
		t.Fatalf("handoff: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}
	sameTree(t, before, tree(t, state))

	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"genesis", "list", "--state-dir", empty}); code != 0 || ta.out.Len() != 0 ||
		!strings.Contains(ta.err.String(), "no finished interview holds a plan") {
		t.Fatalf("an empty state folder: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"genesis", "handoff", "000000", "--state-dir", empty}); code != 3 {
		t.Fatalf("an empty state folder: exit %d: %q", code, ta.err.String())
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("the empty state folder gained %v (%v)", entries, err)
	}

	// A folder the store refuses is a failure, in the store's fixed words and
	// with no path.
	if err := os.Chmod(genesisDir(state), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"genesis", "list"}, {"genesis", "handoff", idTool[:6]}} {
		ta = newTestApp(t, env)
		if code := ta.run(args); code != leaddaemon.ExitFailure || ta.out.Len() != 0 ||
			!strings.Contains(ta.err.String(), "writable by group or other") || strings.Contains(ta.err.String(), base) {
			t.Errorf("%v: exit %d: %q", args, code, ta.err.String())
		}
	}
}

func TestGenesisUsage(t *testing.T) {
	for _, args := range [][]string{
		{"genesis"}, {"genesis", "show"}, {"genesis", "list", "extra"}, {"genesis", "list", "--no-such"},
		{"genesis", "handoff"}, {"genesis", "handoff", "--state-dir", "/x"}, {"genesis", "handoff", "a1b2c3", "extra"},
		{"genesis", "list", "--state-dir"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage || ta.out.Len() != 0 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	for _, args := range [][]string{{"genesis", "list", "--state-dir", "relative/s"}, {"genesis", "handoff", "a1b2c3", "--state-dir", "/a/../b"}} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitConfig {
			t.Errorf("%v: exit %d, want %d", args, code, leaddaemon.ExitConfig)
		}
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"genesis", "list"}); code != leaddaemon.ExitConfig {
		t.Errorf("no state folder: exit %d", code)
	}
	for _, want := range []string{"ataniqd genesis list [--state-dir DIR]", "ataniqd genesis handoff TAG [--state-dir DIR]", "qargi project new NAME --methodology", "needs your own words", "3 refused"} {
		if !strings.Contains(ownerUsage, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}
