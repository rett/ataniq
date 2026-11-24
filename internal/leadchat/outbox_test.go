//go:build linux && ataniq_canary

package leadchat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIngestTakesRepliesInNameOrder(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("0002-second.txt", "Second.\n")
	e.reply("0001-first.txt", "First.")
	rep := ingest(t, s)
	if rep.Taken != 2 || rep.Waiting != 0 || rep.Missing {
		t.Fatalf("report %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "First.", "Second.")
	es := read(t, s)
	if !slices.Equal(seqs(es), []int64{1, 2, 3}) || es[1].Who != "lead" || es[0].Who != "system" || es[1].Epoch != 1 {
		t.Fatalf("entries %+v", es)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox still holds %q", n)
	}
	// The file holds the text and a src, never the file's name.
	all := strings.Join(e.lines(testRole), "\n")
	if strings.Contains(all, "0001-first") || !strings.Contains(all, `"src":"`) {
		t.Fatalf("file %s", all)
	}
}

func TestIngestRefusesLinksFIFOsAndSockets(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	outside := filepath.Join(e.dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.outbox, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(e.outbox, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(e.outbox, "b.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A socket is made in a short folder (a socket path is short) and moved in.
	short, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: filepath.Join(short, "c")}); err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	sockets := 0
	if err := os.Rename(filepath.Join(short, "c"), filepath.Join(e.outbox, "c.txt")); err == nil {
		sockets = 1
	}
	// A device needs privilege; it is tried and left out when refused.
	devices := 0
	if unix.Mknod(filepath.Join(e.outbox, "d.txt"), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))) == nil {
		devices = 1
	}
	e.reply("e.txt", "Real reply.")
	rep := ingest(t, s)
	if want := 3 + sockets + devices; rep.Dropped[ReasonRefused] != want || rep.Taken != 1 {
		t.Fatalf("report %+v, want %d refused", rep, want)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox still holds %q", n)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "private" {
		t.Fatalf("the link's target changed: %q, %v", data, err)
	}
	wantTexts(t, s, TextTurnedOn, "Real reply.", dropTexts[ReasonRefused])
}

func TestIngestLeavesFolders(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for _, d := range []string{"d.txt", "sub", ".hidden"} {
		if err := os.Mkdir(filepath.Join(e.outbox, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.outbox, "sub", "x.txt"), []byte("nested"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.reply("r.txt", "Reply.")
	rep := ingest(t, s)
	if rep.Dropped[ReasonFolder] != 3 || rep.Waiting != 1 || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); !slices.Equal(n, []string{".hidden", "d.txt", "sub"}) {
		t.Fatalf("outbox %q", n)
	}
	if _, err := os.Stat(filepath.Join(e.outbox, "sub", "x.txt")); err != nil {
		t.Fatalf("a nested file was touched: %v", err)
	}
	wantTexts(t, s, TextTurnedOn, "Reply.")
}

func TestIngestRefusesHardLinks(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	home := filepath.Dir(e.outbox)
	keep := filepath.Join(home, "notes")
	if err := os.WriteFile(keep, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(keep, filepath.Join(e.outbox, "a.txt")); err != nil {
		t.Fatal(err)
	}
	e.reply("b.txt", "Twice.")
	if err := os.Link(filepath.Join(e.outbox, "b.txt"), filepath.Join(e.outbox, "c.txt")); err != nil {
		t.Fatal(err)
	}
	// a.txt and b.txt have two links when they are judged and are removed;
	// c.txt has one by then (b.txt is gone) and is an ordinary reply.
	rep := ingest(t, s)
	if rep.Dropped[ReasonRefused] != 2 || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "notes" {
		t.Fatalf("the linked file changed: %q, %v", data, err)
	}
	wantTexts(t, s, TextTurnedOn, "Twice.", dropTexts[ReasonRefused])
}

func TestIngestDropsOversize(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", strings.Repeat("x", MaxReplyBytes+1))
	e.reply("b.txt", strings.Repeat("y", MaxReplyBytes))
	rep := ingest(t, s)
	if rep.Dropped[ReasonTooLong] != 1 || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, strings.Repeat("y", MaxReplyBytes), dropTexts[ReasonTooLong])
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox %q", n)
	}
}

func TestIngestWaitsForSettle(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.clock.set(0)
	e.reply("a.txt", "Fresh.")
	rep := ingest(t, s)
	if rep.Taken != 0 || rep.Waiting != 1 {
		t.Fatalf("an unsettled reply was taken: %+v", rep)
	}
	e.clock.set(SettleAge + time.Second)
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("a settled reply was not taken: %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Fresh.")
}

func TestIngestSkipsAFileThatGrows(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Part one")
	grown := false
	s.ob.afterRead = func(name string) {
		if grown {
			return
		}
		grown = true
		f, err := os.OpenFile(filepath.Join(e.outbox, name), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = f.WriteString(", part two.")
		f.Close()
	}
	if rep := ingest(t, s); rep.Taken != 0 || rep.Waiting != 1 {
		t.Fatalf("a growing file was taken: %+v", rep)
	}
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("the settled file was not taken: %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Part one, part two.")
}

func TestIngestKeepsAReplacedReply(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "First version.")
	replaced := false
	s.ob.beforeRemove = func(name string) {
		if replaced {
			return
		}
		replaced = true
		e.reply(name, "Second version.")
	}
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); !slices.Equal(n, []string{"a.txt"}) {
		t.Fatalf("the replacement was removed: %q", n)
	}
	if rep := ingest(t, s); rep.Taken != 1 || rep.Repaired != 0 {
		t.Fatalf("report %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "First version.", "Second version.")
}

func TestIngestDropsTextThatIsNotPlain(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	bad := []string{"tab\there", "carriage\rreturn", "bad \xff byte", "bidi ‮ text", "   "}
	for i, b := range bad {
		e.reply(fmt.Sprintf("%d.txt", i), b)
	}
	e.reply("z.txt", "Fine.")
	rep := ingest(t, s)
	if rep.Dropped[ReasonNotPlain] != len(bad) || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	// One entry for the reason within a minute, however many drops.
	e.reply("y.txt", "tab\tagain")
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn, "Fine.", dropTexts[ReasonNotPlain])
	e.clock.add(time.Minute)
	e.reply("x.txt", "tab\tonce more")
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn, "Fine.", dropTexts[ReasonNotPlain], dropTexts[ReasonNotPlain])
	if st, _ := s.Status(); st.Dropped[ReasonNotPlain] != len(bad)+2 {
		t.Fatalf("counts %+v", st.Dropped)
	}
}

func TestIngestWithholdsSecretShapedReplies(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	samples := syntheticSecrets(t)
	i := 0
	for _, text := range samples {
		e.reply(fmt.Sprintf("%d.txt", i), text)
		i++
	}
	rep := ingest(t, s)
	if rep.Withheld != len(samples) || rep.Taken != 0 {
		t.Fatalf("report %+v", rep)
	}
	for _, es := range read(t, s)[1:] {
		if es.Who != "system" || es.Text != TextWithheld {
			t.Fatalf("entry %+v", es)
		}
	}
	file := strings.Join(e.lines(testRole), "\n")
	for name, text := range samples {
		for _, part := range strings.Fields(text) {
			if len(part) > 12 && strings.Contains(file, part) {
				t.Fatalf("the %s sample reached the file", name)
			}
		}
	}
}

func TestIngestFloodPastTheEntryCap(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxDirEntries+44; i++ {
		if err := os.WriteFile(filepath.Join(e.outbox, fmt.Sprintf("Bad%03d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep := ingest(t, s)
	if rep.Dropped[ReasonName] != MaxDirEntries || rep.Dropped[ReasonOverflow] != 1 {
		t.Fatalf("first scan %+v", rep)
	}
	rep = ingest(t, s)
	if rep.Dropped[ReasonName] != 44 || rep.Dropped[ReasonOverflow] != 0 {
		t.Fatalf("second scan %+v", rep)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox holds %d", len(n))
	}
	wantTexts(t, s, TextTurnedOn, dropTexts[ReasonName])
}

func TestIngestFloodPastWaitingAndRate(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxWaiting+8; i++ {
		e.reply(fmt.Sprintf("%02d.txt", i), fmt.Sprintf("Reply %d.", i))
	}
	rep := ingest(t, s)
	if rep.Dropped[ReasonFull] != 8 || rep.Taken != RatePerMinute || rep.Waiting != MaxWaiting-RatePerMinute {
		t.Fatalf("report %+v", rep)
	}
	// The excess removed is the end of the name order.
	if n := e.names(); n[len(n)-1] != fmt.Sprintf("%02d.txt", MaxWaiting-1) {
		t.Fatalf("outbox %q", n)
	}
	// Within the minute nothing more is taken.
	if rep := ingest(t, s); rep.Taken != 0 {
		t.Fatalf("taken past the minute's rate: %+v", rep)
	}
	for i := 0; i < 10 && len(e.names()) > 0; i++ {
		e.clock.add(time.Minute)
		ingest(t, s)
	}
	es := read(t, s)
	var lead []string
	for _, e := range es {
		if e.Who == "lead" {
			lead = append(lead, e.Text)
		}
	}
	if len(es) != 1+MaxWaiting+1 || len(lead) != MaxWaiting {
		t.Fatalf("entries %q", texts(es))
	}
	for i, text := range lead {
		if text != fmt.Sprintf("Reply %d.", i) {
			t.Fatalf("order %q", lead)
		}
	}
}

func TestIngestHourlyRate(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	now := e.clock.now()
	for i := 0; i < RatePerHour-1; i++ {
		s.taken = append(s.taken, now.Add(-30*time.Minute))
	}
	for i := 0; i < 3; i++ {
		e.reply(fmt.Sprintf("%d.txt", i), "Reply.")
	}
	if rep := ingest(t, s); rep.Taken != 1 || rep.Waiting != 2 {
		t.Fatalf("report %+v", rep)
	}
	e.clock.add(31 * time.Minute)
	if rep := ingest(t, s); rep.Taken != 2 {
		t.Fatalf("report after the hour %+v", rep)
	}
}

func TestIngestRemovesStaleDotNames(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	if err := os.WriteFile(filepath.Join(e.outbox, ".draft"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := ingest(t, s); len(e.names()) != 1 || rep.Taken != 0 {
		t.Fatalf("a fresh dot-name was touched: %+v", rep)
	}
	e.clock.add(StaleAge + time.Second)
	if rep := ingest(t, s); rep.Dropped[ReasonStale] != 1 || len(e.names()) != 0 {
		t.Fatalf("report %+v, outbox %q", rep, e.names())
	}
	wantTexts(t, s, TextTurnedOn)
}

func TestCrashBetweenAppendAndRemove(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Once.")
	crash := errors.New("synthetic crash")
	s.afterAppend = func() error { return crash }
	if _, err := s.Ingest(1); !errors.Is(err, crash) {
		t.Fatalf("Ingest: %v", err)
	}
	if n := e.names(); !slices.Equal(n, []string{"a.txt"}) {
		t.Fatalf("outbox %q", n)
	}
	s.afterAppend = nil
	rep := ingest(t, s)
	if rep.Repaired != 1 || rep.Taken != 0 || len(e.names()) != 0 {
		t.Fatalf("report %+v, outbox %q", rep, e.names())
	}
	wantTexts(t, s, TextTurnedOn, "Once.")
}

func TestCrashBetweenAppendAndRemoveThenRestart(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Once.")
	s.afterAppend = func() error { return errors.New("synthetic crash") }
	_, _ = s.Ingest(1)
	_ = s.Close()
	s = e.open()
	if st, _ := s.Status(); st.Chat != "off" || st.Entries != 0 {
		t.Fatalf("status after restart %+v", st)
	}
	if _, err := s.SetSwitch("read", 2); err != nil {
		t.Fatal(err)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("the turn-on did not clear %q", n)
	}
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn)
}

func TestOutboxRefusesLinkedFolders(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			e := newEnv(t)
			if fallback {
				e.root.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
			}
			s := e.on()
			elsewhere := filepath.Join(e.dir, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(elsewhere, "a.txt"), []byte("Planted."), 0o600); err != nil {
				t.Fatal(err)
			}
			// The outbox itself a link.
			if err := os.RemoveAll(e.outbox); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, e.outbox); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Ingest(1); !errors.Is(err, ErrInsecure) {
				t.Fatalf("Ingest through a linked outbox: %v", err)
			}
			if _, err := ClearOutbox(e.root, testRole); !errors.Is(err, ErrInsecure) {
				t.Fatalf("ClearOutbox through a linked outbox: %v", err)
			}
			// The home a link, with a real outbox below its target.
			home := filepath.Dir(e.outbox)
			if err := os.RemoveAll(home); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(elsewhere, OutboxName), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, home); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Ingest(1); !errors.Is(err, ErrInsecure) {
				t.Fatalf("Ingest through a linked home: %v", err)
			}
			if _, err := os.Stat(filepath.Join(elsewhere, "a.txt")); err != nil {
				t.Fatalf("the planted file was touched: %v", err)
			}
			wantTexts(t, s, TextTurnedOn)
		})
	}
}

func TestOutboxRefusesAWritableFolder(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	if err := os.Chmod(e.outbox, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(1); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Ingest: %v", err)
	}
}

func TestMissingOutbox(t *testing.T) {
	e := newEnv(t)
	if err := os.Remove(e.outbox); err != nil {
		t.Fatal(err)
	}
	s := e.on() // the clear finds nothing to clear
	if rep := ingest(t, s); !rep.Missing {
		t.Fatalf("report %+v", rep)
	}
}

func TestScanOutboxAlone(t *testing.T) {
	e := newEnv(t)
	e.reply("a.txt", "One.")
	e.reply("b.txt", "Two.")
	b, err := ScanOutbox(e.root, testRole, 1, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(b.Replies) != 1 || b.Waiting != 1 || string(b.Replies[0].Data) != "One." || !validSrc(b.Replies[0].Src) {
		t.Fatalf("batch %+v", b)
	}
	if ok, err := b.Remove(b.Replies[0]); !ok || err != nil {
		t.Fatalf("Remove: %v, %v", ok, err)
	}
	if ok, err := b.Remove(b.Replies[0]); ok || err != nil {
		t.Fatalf("second Remove: %v, %v", ok, err)
	}
	if _, err := ScanOutbox(e.root, "Not/A/Role", 1, e.clock.now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}
}

func TestOpenRootRefuses(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(link); !errors.Is(err, ErrInsecure) {
		t.Fatalf("linked root: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(dir); !errors.Is(err, ErrInsecure) {
		t.Fatalf("writable root: %v", err)
	}
}
