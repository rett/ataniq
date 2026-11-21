//go:build linux && ataniq_canary

package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadholder/screenrec"
	"ataniq/internal/leadholder/serve"
)

// recordDir is the folder the daemon gives a recording holder.
func (c *canaryRig) recordDir() string { return filepath.Join(c.runtime, screenrec.DirName) }

// recordScript is an agent that sets a title, draws a screen and sends the
// support query and one report of the status protocol.
func recordScript() string {
	screenBytes := "\x1b]2;sample title\x07\x1b[2J\x1b[Hhello recorder"
	return "raw; print " + hex.EncodeToString([]byte(screenBytes)) + "; print " + hexOSC("?") +
		"; print " + hexOSC("state=idle") + "; wait"
}

// fastRecorder makes the recorder look often, so that a test does not wait
// for the real intervals, and hands the test the recorder.
func fastRecorder(d *deps, got chan<- *screenrec.Recorder) {
	d.canary.tweakRecorder = func(c *screenrec.Config) {
		c.PollInterval, c.PairInterval = 5*time.Millisecond, 20*time.Millisecond
	}
	d.canary.recorderStarted = func(r *screenrec.Recorder) { got <- r }
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The flag is refused without --canary, and a value that is not an absolute
// path of plain text is refused; each refusal starts nothing.
func TestCanaryRecordDirFlagRules(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *canaryRig, a *argSpec)
		want  string
	}{
		{"with --scratch", func(c *canaryRig, a *argSpec) {
			a.scratch, a.extraBefore = true, nil
			a.drop("sandbox-inputs").drop("expected-mode").set("record-dir", c.recordDir())
		}, "--record-dir is a flag of canary mode: it needs --canary"},
		{"with neither mode", func(c *canaryRig, a *argSpec) {
			a.extraBefore = nil
			a.drop("sandbox-inputs").drop("expected-mode").set("record-dir", c.recordDir())
		}, "--record-dir is a flag of canary mode: it needs --canary"},
		{"relative", func(c *canaryRig, a *argSpec) { a.set("record-dir", "record") }, "--record-dir must be an absolute path"},
		{"empty", func(c *canaryRig, a *argSpec) { a.set("record-dir", "") }, "--record-dir must be an absolute path"},
		{"twice", func(c *canaryRig, a *argSpec) {
			a.set("record-dir", c.recordDir())
			a.flags = append(a.flags, [2]string{"record-dir", c.recordDir()})
		}, "--record-dir was given twice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("")
			tc.setup(c, a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			untouched(t, c.runtime)
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused command line reached the start sequence")
			}
		})
	}
}

// --record-dir is exactly the folder record in --runtime-dir, and one that
// exists is refused: an earlier recording is neither added to nor replaced.
// Both are refused before the role lock, writing nothing.
func TestCanaryRecordDirRefusalsBeforeTheLock(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *canaryRig, a *argSpec)
		want  string
	}{
		{"another name", func(c *canaryRig, a *argSpec) {
			a.set("record-dir", filepath.Join(c.runtime, "other"))
		}, "--record-dir is not the folder record in --runtime-dir"},
		{"under the state folder", func(c *canaryRig, a *argSpec) {
			a.set("record-dir", filepath.Join(c.state, screenrec.DirName))
		}, "--record-dir is not the folder record in --runtime-dir"},
		{"not clean", func(c *canaryRig, a *argSpec) {
			a.set("record-dir", c.runtime+"/x/../"+screenrec.DirName)
		}, "--record-dir is not the folder record in --runtime-dir"},
		{"exists", func(c *canaryRig, a *argSpec) {
			if err := os.Mkdir(c.recordDir(), 0o700); err != nil {
				c.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.recordDir(), "0001.txt"), []byte("earlier\n"), 0o600); err != nil {
				c.t.Fatal(err)
			}
			a.set("record-dir", c.recordDir())
		}, "--record-dir exists already"},
		{"a link", func(c *canaryRig, a *argSpec) {
			if err := os.Symlink(c.base, c.recordDir()); err != nil {
				c.t.Fatal(err)
			}
			a.set("record-dir", c.recordDir())
		}, "--record-dir exists already"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("")
			tc.setup(c, a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			if strings.Contains(c.errbuf.String(), c.base) {
				t.Errorf("the refusal names a path: %s", c.errbuf.String())
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			if c.systemsMade() != 0 || len(c.launches()) != 0 || !c.lockFree() {
				t.Error("a refused command line reached the start sequence")
			}
			if tc.name == "exists" && readFile(t, filepath.Join(c.recordDir(), "0001.txt")) != "earlier\n" {
				t.Error("the earlier recording was changed")
			}
		})
	}
}

// With --record-dir the holder records: the folder is made 0700 once the
// start succeeded, its files are 0600, a pair holds the screen, the title and
// both payloads are recorded, the hook still answers and counts, the folder is
// a holder path the sandbox must not show, and stderr names the folder by its
// relative name only.
func TestCanaryRecordsScreensTitlesAndReports(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.script = recordScript()
	recs := make(chan *screenrec.Recorder, 1)
	d := c.deps()
	fastRecorder(&d, recs)
	go func() { c.done <- run(c.spec(digest).set("record-dir", c.recordDir()).build(), d) }()
	c.waitReady()
	<-recs

	dir := c.recordDir()
	poll(t, func() string { return "the first pair" }, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "0001.txt"))
		return err == nil && strings.Contains(string(b), "hello recorder")
	})
	poll(t, func() string { return "the two payloads" }, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, screenrec.OSCName))
		return strings.Count(string(b), "\n") == 2
	})
	poll(t, func() string { return "the title" }, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, screenrec.TitlesName))
		return strings.Contains(string(b), `"title":"sample title"`)
	})
	poll(t, func() string { return "the counted query" }, func() bool {
		return c.status().Counters[counterQueries] == 1 && c.status().Counters[counterReports] == 1
	})
	// The holder waited for the recorder before it exited: nothing is
	// closed here.
	c.stopAndWait()

	osc := readFile(t, filepath.Join(dir, screenrec.OSCName))
	if !strings.Contains(osc, `"payload":"?"`) || !strings.Contains(osc, `"payload":"state=idle"`) {
		t.Errorf("osc7501.jsonl %q", osc)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "0001.json")), `"title": "sample title"`) {
		t.Errorf("0001.json %s", readFile(t, filepath.Join(dir, "0001.json")))
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, screenrec.ReadmeName)), "raw, scrub before use; do not commit") {
		t.Error("README.txt does not say the files are raw")
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode().Perm() != 0o700 || !fi.IsDir() {
		t.Errorf("the folder: %v %v", fi, err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		fi, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", e.Name(), fi, err)
		}
	}

	stderr := c.errbuf.String()
	if strings.Count(stderr, "recording screens to record\n") != 1 {
		t.Errorf("stderr does not say once where it records: %s", stderr)
	}
	if strings.Contains(stderr, dir) {
		t.Errorf("stderr names the folder's path: %s", stderr)
	}
	seen := c.sys.inputs()
	if len(seen) != 1 {
		t.Fatalf("%d starts", len(seen))
	}
	want := []string{filepath.Join(c.runtime, serve.AttachSocket), filepath.Join(c.runtime, serve.ControlSocket), dir}
	if !reflect.DeepEqual(seen[0].HolderPaths, want) {
		t.Errorf("HolderPaths %q, want %q", seen[0].HolderPaths, want)
	}
	if ls := c.launches(); len(ls) != 1 || len(ls[0].OSC) != 1 {
		t.Errorf("the session's hooks: %+v", ls)
	}
	// Nothing of the recording is in the event log or the state folder.
	if strings.Contains(string(c.logBytes()), screenrec.DirName) {
		t.Error("the event log names the recording")
	}
}

// Without --record-dir nothing is recorded: no folder, no line, and the start
// is the one of a holder with no recorder (the mutation check below compares
// the two).
func TestCanaryRecordingOffWritesNothing(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.script = recordScript()
	c.start(c.spec(digest).build())
	c.waitReady()
	poll(t, func() string { return "the counted query" }, func() bool { return c.status().Counters[counterQueries] == 1 })
	time.Sleep(200 * time.Millisecond)
	c.stopAndWait()
	if _, err := os.Lstat(c.recordDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a folder was made without --record-dir: %v", err)
	}
	untouched(t, c.runtime, "bound", "report")
	if strings.Contains(c.errbuf.String(), "recording") {
		t.Errorf("stderr speaks of recording: %s", c.errbuf.String())
	}
}

// The mutation check of recording off: one rig starts the same holder twice,
// without and with --record-dir. The plan's argument vector, its environment,
// its folder and its terminal settings are the same; the holder paths are the
// golden pair of sockets without the flag and that pair and the folder with
// it; the session has one hook either way. So the flag adds the folder and
// nothing else to the start, and leaving it out changes none of it.
func TestCanaryRecordingOffChangesNoGoldenOfTheStart(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	c.script = "wait"

	c.start(c.spec(digest).build())
	c.waitReady()
	c.stopAndWait()
	// A first start again: the mark of the first is removed.
	if err := os.Remove(filepath.Join(c.state, leadHasRunMark)); err != nil {
		t.Fatal(err)
	}
	c.errbuf = &syncBuf{}
	c.done = make(chan int, 1)
	recs := make(chan *screenrec.Recorder, 1)
	d := c.deps()
	fastRecorder(&d, recs)
	go func() { c.done <- run(c.spec(digest).set("record-dir", c.recordDir()).build(), d) }()
	c.waitReady()
	<-recs
	c.stopAndWait()

	ls, seen := c.launches(), c.sys.inputs()
	if len(ls) != 2 || len(seen) != 2 {
		t.Fatalf("%d launches and %d starts", len(ls), len(seen))
	}
	off, on := ls[0], ls[1]
	if !reflect.DeepEqual(off.Argv, on.Argv) || !reflect.DeepEqual(off.Env, on.Env) || off.Dir != on.Dir ||
		!reflect.DeepEqual(off.PTY, on.PTY) || off.StopGrace != on.StopGrace || len(off.OSC) != 1 || len(on.OSC) != 1 || off.OSC[0].Cmd != on.OSC[0].Cmd {
		t.Errorf("the launch differs:\noff %+v\non  %+v", off, on)
	}
	golden := []string{filepath.Join(c.runtime, serve.AttachSocket), filepath.Join(c.runtime, serve.ControlSocket)}
	if !reflect.DeepEqual(seen[0].HolderPaths, golden) {
		t.Errorf("HolderPaths without the flag %q, want the golden %q", seen[0].HolderPaths, golden)
	}
	if !reflect.DeepEqual(seen[1].HolderPaths, append(append([]string(nil), golden...), c.recordDir())) {
		t.Errorf("HolderPaths with the flag %q", seen[1].HolderPaths)
	}
	a, b := seen[0], seen[1]
	a.HolderPaths, b.HolderPaths = nil, nil
	if !reflect.DeepEqual(a, b) {
		t.Errorf("the inputs differ beyond the holder paths:\n%+v\n%+v", a, b)
	}
}

// A start that fails makes no folder: the recorder makes it only once the
// sandbox has started.
func TestCanaryFailedStartMakesNoRecording(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.sys.fail["ReadLine"] = errors.New("sample failure of the report line")
	code := c.runNow(c.spec(digest).set("record-dir", c.recordDir()).build())
	if code != exitSandbox {
		t.Fatalf("exit %d, want %d; stderr: %s", code, exitSandbox, c.errbuf.String())
	}
	if _, err := os.Lstat(c.recordDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed start made the folder: %v", err)
	}
	if strings.Contains(c.errbuf.String(), "recording screens") {
		t.Errorf("stderr: %s", c.errbuf.String())
	}
	c.noAgentNoClaim()
}

// A folder that cannot be made at the start does not stop the agent: one line
// says so, with no path, and the holder serves as without the flag.
func TestCanaryRecorderThatCannotOpenChangesNothingElse(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.script = recordScript()
	var once sync.Once
	// Something makes the folder after the holder's check and before the
	// recorder's: the start sequence's first step stands in for it.
	c.sys.hook = map[string]func(){"CheckHost": func() {
		once.Do(func() {
			if err := os.Mkdir(c.recordDir(), 0o700); err != nil {
				t.Error(err)
			}
		})
	}}
	c.start(c.spec(digest).set("record-dir", c.recordDir()).build())
	c.waitReady()
	poll(t, func() string { return "the counted query" }, func() bool { return c.status().Counters[counterQueries] == 1 })
	c.stopAndWait()
	stderr := c.errbuf.String()
	if !strings.Contains(stderr, "not recording screens: make the folder: file exists") {
		t.Errorf("stderr: %s", stderr)
	}
	if strings.Contains(stderr, "recording screens to") || strings.Contains(stderr, c.recordDir()) {
		t.Errorf("stderr: %s", stderr)
	}
	if ents, _ := os.ReadDir(c.recordDir()); len(ents) != 0 {
		t.Errorf("written into a folder the recorder did not make: %v", ents)
	}
}

// The holder waits for the recorder when the session is over: the screen shown
// at the end, which came within the pair interval of the first and so only the
// last look takes, and the payload queued last are on disk when run returns.
// The last pair carries the cursor, the alternate screen, the title and the
// faint spans, so final.json is not needed for them. The test does not close
// the recorder itself.
func TestCanaryRecordingIsFlushedWhenTheSessionEnds(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	first := "\x1b]2;first title\x07\x1b[2J\x1b[Hfirst screen"
	last := "\x1b[?1049h\x1b[2J\x1b[Hlast screen \x1b[2mfaint hint\x1b[22m\x1b[2;5H\x1b]2;last title\x07"
	c.script = "raw; print " + hex.EncodeToString([]byte(first)) + "; print " + hexOSC("state=idle") +
		"; sleep 300ms; print " + hex.EncodeToString([]byte(last)) + "; print " + hexOSC("state=working") + "; wait"
	recs := make(chan *screenrec.Recorder, 1)
	d := c.deps()
	d.canary.tweakRecorder = func(c *screenrec.Config) {
		// One pair an hour: after the first, only the look at the end takes one.
		c.PollInterval, c.PairInterval = 5*time.Millisecond, time.Hour
	}
	d.canary.recorderStarted = func(r *screenrec.Recorder) { recs <- r }
	go func() { c.done <- run(c.spec(digest).set("record-dir", c.recordDir()).build(), d) }()
	c.waitReady()
	<-recs
	dir := c.recordDir()
	poll(t, func() string { return "the first pair" }, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "0001.txt"))
		return err == nil && strings.Contains(string(b), "first screen")
	})
	poll(t, func() string { return "the second report" }, func() bool { return c.status().Counters[counterReports] == 2 })
	c.stopAndWait()

	if got := readFile(t, filepath.Join(dir, "0002.txt")); !strings.HasPrefix(got, "last screen faint hint\n") {
		t.Fatalf("0002.txt %q, want the last screen", got)
	}
	var pair struct {
		CursorX   int    `json:"cursor_x"`
		CursorY   int    `json:"cursor_y"`
		AltScreen bool   `json:"alt_screen"`
		Title     string `json:"title"`
		Faint     []struct {
			Row, From, To int
		} `json:"faint"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "0002.json"))), &pair); err != nil {
		t.Fatal(err)
	}
	if pair.CursorX != 4 || pair.CursorY != 1 || !pair.AltScreen || pair.Title != "last title" ||
		len(pair.Faint) != 1 || pair.Faint[0].Row != 0 || pair.Faint[0].From != len("last screen ") {
		t.Errorf("0002.json %+v", pair)
	}
	if _, err := os.Lstat(filepath.Join(dir, "0003.txt")); err == nil {
		t.Error("a third pair was written")
	}
	osc := readFile(t, filepath.Join(dir, screenrec.OSCName))
	if strings.Count(osc, "\n") != 2 || !strings.Contains(osc, `"payload":"state=working"`) {
		t.Errorf("osc7501.jsonl %q", osc)
	}
	if titles := readFile(t, filepath.Join(dir, screenrec.TitlesName)); !strings.Contains(titles, `"title":"last title"`) {
		t.Errorf("titles.jsonl %q", titles)
	}
}
