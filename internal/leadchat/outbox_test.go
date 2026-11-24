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
	if rep.Taken != 2 || rep.Waiting != 0 || rep.Suspended {
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
	if c := e.claims(); len(c) != 0 {
		t.Fatalf("claim folder still holds %q", c)
	}
	// The file holds the text and a claim id, never the file's name.
	all := strings.Join(e.lines(testRole), "\n")
	if strings.Contains(all, "0001-first") || !strings.Contains(all, `"claim":1}`) || !strings.Contains(all, `"claim":2}`) {
		t.Fatalf("file %s", all)
	}
}

// The same name with the same bytes, written twice, is two replies.
func TestIngestTakesTheSameReplyTwice(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Same.")
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("first %+v", rep)
	}
	e.reply("a.txt", "Same.")
	if rep := ingest(t, s); rep.Taken != 1 || rep.Repaired != 0 {
		t.Fatalf("second %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Same.", "Same.")
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
	if want := 2 + sockets + devices; rep.Dropped[ReasonRefused] != want || rep.Dropped[ReasonName] != 1 || rep.Taken != 1 {
		t.Fatalf("report %+v, want %d refused", rep, want)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox still holds %q", n)
	}
	if c := e.claims(); len(c) != 0 {
		t.Fatalf("claim folder still holds %q", c)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "private" {
		t.Fatalf("the link's target changed: %q, %v", data, err)
	}
	wantTexts(t, s, TextTurnedOn, "Real reply.", dropTexts[ReasonRefused], dropTexts[ReasonName])
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
	if rep.Dropped[ReasonFolder] != 3 || rep.Waiting != 3 || rep.Taken != 1 {
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

// Folders are never removed, so they count toward MaxWaiting for good: an
// outbox whose folders and replies pass it stops the chat, and a turn-on
// refuses an outbox with more folders than it.
func TestFoldersCountTowardWaiting(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxWaiting-1; i++ {
		if err := os.Mkdir(filepath.Join(e.outbox, fmt.Sprintf("d%02d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.reply("a.txt", "One.")
	e.reply("b.txt", "Two.")
	rep := ingest(t, s)
	if !rep.Suspended || rep.Taken != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); len(n) != MaxWaiting+1 {
		t.Fatalf("the outbox was touched: %d entries", len(n))
	}
	if err := os.Mkdir(filepath.Join(e.outbox, "d99"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.outbox, "d98"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrFull) {
		t.Fatalf("SetSwitch read with %d folders: %v", MaxWaiting+1, err)
	}
	if st, _ := s.Status(); st.Chat != "off" {
		t.Fatalf("status %+v", st)
	}
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
	// c.txt has one by then (the claim of b.txt was removed) and is an
	// ordinary reply. Removing that claim set c.txt's change time to the
	// kernel's now, and c.txt is still taken in the same scan only because
	// the test clock runs SettleAge and more ahead of the kernel's.
	rep := ingest(t, s)
	if rep.Dropped[ReasonRefused] != 2 || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "notes" {
		t.Fatalf("the linked file changed: %q, %v", data, err)
	}
	wantTexts(t, s, TextTurnedOn, "Twice.", dropTexts[ReasonRefused])
}

// A reply that group or other may write is refused; one they may only read
// is taken: the lead's umask is not the daemon's.
func TestIngestReplyModes(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Group may write.")
	e.reply("b.txt", "Others may write.")
	e.reply("c.txt", "Others may read.")
	for name, mode := range map[string]os.FileMode{"a.txt": 0o620, "b.txt": 0o602, "c.txt": 0o644} {
		if err := os.Chmod(filepath.Join(e.outbox, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	rep := ingest(t, s)
	if rep.Dropped[ReasonRefused] != 2 || rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Others may read.", dropTexts[ReasonRefused])
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
	if n := e.names(); !slices.Equal(n, []string{"a.txt"}) {
		t.Fatalf("an unsettled reply was claimed: %q", n)
	}
	e.clock.set(SettleAge + time.Second)
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("a settled reply was not taken: %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Fresh.")
}

// A claimed reply that a writer that held it open changes while it is read
// is kept as a claim and read again, once it settled, by a later scan.
func TestIngestRereadsAClaimThatGrows(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Part one")
	grown := false
	s.afterRead = func(name string) {
		if grown {
			return
		}
		grown = true
		f, err := os.OpenFile(filepath.Join(e.claimDir(), name), os.O_APPEND|os.O_WRONLY, 0)
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
	if c := e.claims(); len(c) != 1 {
		t.Fatalf("claim folder %q", c)
	}
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("the settled claim was not taken: %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "Part one, part two.")
}

// A lead that writes its reply's name again after the claim keeps its new
// file: the claim, not the name, is removed.
func TestIngestKeepsAReplyWrittenAfterTheClaim(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "First version.")
	replaced := false
	s.beforeDelete = func(int64) {
		if replaced {
			return
		}
		replaced = true
		e.reply("a.txt", "Second version.")
	}
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); !slices.Equal(n, []string{"a.txt"}) {
		t.Fatalf("the new reply was removed: %q", n)
	}
	if rep := ingest(t, s); rep.Taken != 1 || rep.Repaired != 0 {
		t.Fatalf("report %+v", rep)
	}
	wantTexts(t, s, TextTurnedOn, "First version.", "Second version.")
}

// A name replaced between the scan's look at it and the claim: the claim is
// the new file, which is read and stored, and nothing is lost.
func TestIngestClaimsAReplyReplacedBeforeTheClaim(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "First version.")
	s.beforeClaim = func(name string) {
		if name == "a.txt" {
			s.beforeClaim = nil
			e.reply("a.txt", "Second version.")
		}
	}
	if rep := ingest(t, s); rep.Taken != 1 {
		t.Fatalf("report %+v", rep)
	}
	if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
		t.Fatalf("outbox %q, claims %q", n, c)
	}
	wantTexts(t, s, TextTurnedOn, "Second version.")
}

// A folder that takes a reply's name just before the claim is claimed, never
// read, and removed with what it holds.
func TestIngestRemovesAFolderClaimedInARace(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Swapped away.")
	s.beforeClaim = func(name string) {
		s.beforeClaim = nil
		p := filepath.Join(e.outbox, name)
		if err := os.Remove(p); err != nil {
			t.Error(err)
		}
		if err := os.MkdirAll(filepath.Join(p, "deep", "er"), 0o700); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(p, "deep", "x.txt"), []byte("x"), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("/", filepath.Join(p, "root")); err != nil {
			t.Error(err)
		}
	}
	rep := ingest(t, s)
	if rep.Dropped[ReasonRefused] != 1 || rep.Taken != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
		t.Fatalf("outbox %q, claims %q", n, c)
	}
}

func TestIngestDropsTextThatIsNotPlain(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	bad := []string{"tab\there", "carriage\rreturn", "bad \xff byte", "bidi \u202e text", "   "}
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
		if es.Who != "system" || es.Text != TextWithheld || es.claim == 0 {
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

// An outbox of more than MaxDirEntries entries stops the chat with one
// entry, before anything in it is touched; a fresh turn-on clears it.
func TestIngestFloodPastTheEntryCap(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxDirEntries+1; i++ {
		if err := os.WriteFile(filepath.Join(e.outbox, fmt.Sprintf("Bad%03d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.reply("a.txt", "Lost in the flood.")
	rep := ingest(t, s)
	if !rep.Suspended || rep.Taken != 0 || len(rep.Dropped) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); len(n) != MaxDirEntries+2 {
		t.Fatalf("the outbox was touched: %d entries", len(n))
	}
	wantTexts(t, s, TextTurnedOn, TextStoppedFull)
	if _, err := s.Ingest(1); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Ingest while suspended: %v", err)
	}
	wantTexts(t, s, TextTurnedOn, TextStoppedFull)
	if st, _ := s.Status(); st.Chat != "read" || !st.Suspended {
		t.Fatalf("status %+v", st)
	}
	st, err := s.SetSwitch("read", 2)
	if err != nil || st.Suspended || st.Gen != 3 {
		t.Fatalf("a fresh turn-on: %+v, %v", st, err)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("the fresh turn-on left %d entries", len(n))
	}
	wantTexts(t, s, TextTurnedOn)
}

// More than MaxWaiting replies stop the chat with one entry; none is cut.
func TestIngestFloodPastWaiting(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxWaiting+1; i++ {
		e.reply(fmt.Sprintf("%02d.txt", i), fmt.Sprintf("Reply %d.", i))
	}
	rep := ingest(t, s)
	if !rep.Suspended || rep.Taken != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); len(n) != MaxWaiting+1 {
		t.Fatalf("outbox %d entries", len(n))
	}
	wantTexts(t, s, TextTurnedOn, TextStoppedFull)
}

func TestIngestRate(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxWaiting; i++ {
		e.reply(fmt.Sprintf("%02d.txt", i), fmt.Sprintf("Reply %d.", i))
	}
	rep := ingest(t, s)
	if rep.Taken != RatePerMinute || rep.Waiting != MaxWaiting-RatePerMinute {
		t.Fatalf("report %+v", rep)
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
	if len(es) != 1+MaxWaiting || len(lead) != MaxWaiting {
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

// A file whose change time is before the turn-on found the outbox empty is
// removed and never read, whatever its name.
func TestIngestRemovesWhatPredatesTheTurnOn(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "From before.")
	if err := os.WriteFile(filepath.Join(e.outbox, ".draft"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	// As if both were written before the turn-on and the clear missed them.
	s.since = time.Now().Add(time.Hour)
	rep := ingest(t, s)
	if rep.Dropped[ReasonBefore] != 2 || rep.Taken != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox %q", n)
	}
	wantTexts(t, s, TextTurnedOn)
}

// The crash points of a reply: after its claim, after its entry is stored,
// and a removal of its claim that fails. Each reply is stored once.
func TestCrashPoints(t *testing.T) {
	crash := errors.New("synthetic crash")
	t.Run("after the claim", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Once.")
		s.afterClaim = func(int64) error { return crash }
		if _, err := s.Ingest(1); !errors.Is(err, crash) {
			t.Fatalf("Ingest: %v", err)
		}
		if n, c := e.names(), e.claims(); len(n) != 0 || !slices.Equal(c, []string{"r1"}) {
			t.Fatalf("outbox %q, claims %q", n, c)
		}
		wantTexts(t, s, TextTurnedOn)
		s.afterClaim = nil
		rep := ingest(t, s)
		if rep.Taken != 1 || rep.Repaired != 0 || len(e.claims()) != 0 {
			t.Fatalf("report %+v, claims %q", rep, e.claims())
		}
		wantTexts(t, s, TextTurnedOn, "Once.")
	})
	t.Run("after the append", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Once.")
		s.afterAppend = func() error { return crash }
		if _, err := s.Ingest(1); !errors.Is(err, crash) {
			t.Fatalf("Ingest: %v", err)
		}
		if c := e.claims(); !slices.Equal(c, []string{"r1"}) {
			t.Fatalf("claims %q", c)
		}
		s.afterAppend = nil
		rep := ingest(t, s)
		if rep.Repaired != 1 || rep.Taken != 0 || len(e.claims()) != 0 {
			t.Fatalf("report %+v, claims %q", rep, e.claims())
		}
		wantTexts(t, s, TextTurnedOn, "Once.")
	})
	t.Run("a removal that fails", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Once.")
		fail := errors.New("synthetic removal failure")
		s.unlink = func(dirfd int, name string, flags int) error {
			if name == "r1" {
				return fail
			}
			return unix.Unlinkat(dirfd, name, flags)
		}
		if _, err := s.Ingest(1); !errors.Is(err, fail) {
			t.Fatalf("Ingest: %v", err)
		}
		e.reply("b.txt", "Twice.")
		s.unlink = unix.Unlinkat
		rep := ingest(t, s)
		if rep.Repaired != 1 || rep.Taken != 1 || len(e.claims()) != 0 {
			t.Fatalf("report %+v, claims %q", rep, e.claims())
		}
		wantTexts(t, s, TextTurnedOn, "Once.", "Twice.")
	})
	t.Run("a dropped reply's removal that fails", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "tab\there")
		fail := errors.New("synthetic removal failure")
		s.unlink = func(dirfd int, name string, flags int) error {
			if name == "r1" {
				return fail
			}
			return unix.Unlinkat(dirfd, name, flags)
		}
		if _, err := s.Ingest(1); !errors.Is(err, fail) {
			t.Fatalf("Ingest: %v", err)
		}
		s.unlink = unix.Unlinkat
		rep := ingest(t, s)
		if rep.Repaired != 0 || rep.Taken != 0 || rep.Dropped[ReasonNotPlain] != 1 || len(e.claims()) != 0 {
			t.Fatalf("report %+v, claims %q", rep, e.claims())
		}
	})
	t.Run("a restart after the claim", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Before the restart.")
		e.reply("b.txt", "Also before.")
		s.afterClaim = func(int64) error { return crash }
		_, _ = s.Ingest(1)
		_ = s.Close()
		s = e.open()
		if st, _ := s.Status(); st.Chat != "off" || st.Entries != 0 {
			t.Fatalf("status after restart %+v", st)
		}
		st, err := s.SetSwitch("read", 2)
		if err != nil || st.Chat != "read" {
			t.Fatalf("SetSwitch: %+v, %v", st, err)
		}
		if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
			t.Fatalf("the turn-on left outbox %q, claims %q", n, c)
		}
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn)
	})
}

// The outbox must be the folder the turn-on verified: a replaced, removed or
// opened outbox stops the chat with one entry until a fresh turn-on.
func TestOutboxIdentity(t *testing.T) {
	cases := map[string]func(e *env){
		"replaced": func(e *env) {
			if err := os.Rename(e.outbox, e.outbox+".old"); err != nil {
				e.t.Fatal(err)
			}
			e.makeOutbox(testRole)
		},
		"removed": func(e *env) {
			if err := os.Remove(e.outbox); err != nil {
				e.t.Fatal(err)
			}
		},
		"open to group": func(e *env) {
			if err := os.Chmod(e.outbox, 0o750); err != nil {
				e.t.Fatal(err)
			}
		},
		"closed to the daemon": func(e *env) {
			if err := os.Chmod(e.outbox, 0); err != nil {
				e.t.Fatal(err)
			}
			e.t.Cleanup(func() { _ = os.Chmod(e.outbox, 0o700) })
		},
		"writable by others": func(e *env) {
			if err := os.Chmod(e.outbox, 0o777); err != nil {
				e.t.Fatal(err)
			}
		},
		"a link": func(e *env) {
			elsewhere := filepath.Join(e.dir, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o700); err != nil {
				e.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(elsewhere, "a.txt"), []byte("Planted."), 0o600); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Rename(e.outbox, e.outbox+".old"); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, e.outbox); err != nil {
				e.t.Fatal(err)
			}
		},
		"the home a link": func(e *env) {
			home := filepath.Dir(e.outbox)
			elsewhere := filepath.Join(e.dir, "elsewhere")
			if err := os.Rename(home, elsewhere); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, home); err != nil {
				e.t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := e.on()
			e.reply("a.txt", "Before the change.")
			ingest(t, s)
			change(e)
			rep := ingest(t, s)
			if !rep.Suspended || rep.Taken != 0 {
				t.Fatalf("report %+v", rep)
			}
			if _, err := s.Ingest(1); !errors.Is(err, ErrSuspended) {
				t.Fatalf("Ingest while suspended: %v", err)
			}
			wantTexts(t, s, TextTurnedOn, "Before the change.", TextStoppedFolder)
			if planted := filepath.Join(e.dir, "elsewhere", "a.txt"); name == "a link" {
				if _, err := os.Stat(planted); err != nil {
					t.Fatalf("the planted file was touched: %v", err)
				}
			}
		})
	}
	t.Run("a fresh turn-on goes on with the new folder", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		cases["replaced"](e)
		if rep := ingest(t, s); !rep.Suspended {
			t.Fatalf("report %+v", rep)
		}
		e.reply("b.txt", "Into the new folder.")
		st, err := s.SetSwitch("read", 1)
		if err != nil || st.Suspended || st.Chat != "read" {
			t.Fatalf("SetSwitch: %+v, %v", st, err)
		}
		e.reply("c.txt", "After the fresh turn-on.")
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn, "After the fresh turn-on.")
	})
}

// A turn-on needs an outbox it can verify: one it makes, or one of the
// daemon's user with mode 0700 exactly, reached without a link.
func TestTurnOnVerifiesTheOutbox(t *testing.T) {
	t.Run("made when missing", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Remove(e.outbox); err != nil {
			t.Fatal(err)
		}
		s := e.on()
		if fi, err := os.Lstat(e.outbox); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("outbox %v, %v", fi, err)
		}
		e.reply("a.txt", "Into the new outbox.")
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn, "Into the new outbox.")
	})
	refused := map[string]struct {
		change func(e *env)
		want   error
	}{
		"mode 0750": {func(e *env) {
			if err := os.Chmod(e.outbox, 0o750); err != nil {
				e.t.Fatal(err)
			}
		}, ErrInsecure},
		"a link": {func(e *env) {
			elsewhere := filepath.Join(e.dir, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o700); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Remove(e.outbox); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, e.outbox); err != nil {
				e.t.Fatal(err)
			}
		}, ErrInsecure},
		"no home": {func(e *env) {
			if err := os.RemoveAll(filepath.Dir(e.outbox)); err != nil {
				e.t.Fatal(err)
			}
		}, ErrUnavailable},
		"no openat2": {func(e *env) {
			e.root.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
		}, ErrUnavailable},
		"openat2 denied": {func(e *env) {
			e.root.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.EPERM }
		}, ErrUnavailable},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := e.open()
			c.change(e)
			if _, err := s.SetSwitch("read", 1); !errors.Is(err, c.want) {
				t.Fatalf("SetSwitch read: %v, want %v", err, c.want)
			}
			if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
				t.Fatalf("status %+v, %v", st, err)
			}
		})
	}
	t.Run("no openat2 after the turn-on", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.root.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
		if _, err := s.Ingest(1); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Ingest: %v", err)
		}
	})
}

// A clear reads at most 8192 entries: an outbox with more is not cleared and
// the switch stays off.
func TestTurnOnRefusesAnOutboxOverTheClearLimit(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	for i := 0; i < maxClearEntries+1; i++ {
		f, err := os.Create(filepath.Join(e.outbox, fmt.Sprintf("f%05d", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrFull) {
		t.Fatalf("SetSwitch read: %v", err)
	}
	if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
		t.Fatalf("status %+v, %v", st, err)
	}
	if n := e.names(); len(n) != maxClearEntries+1 {
		t.Fatalf("the outbox was touched: %d entries", len(n))
	}
}

// A clear whose removal fails refuses the turn-on.
func TestTurnOnRefusedWhenTheClearFails(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	e.reply("old.txt", "From before.")
	fail := errors.New("synthetic removal failure")
	s.unlink = func(int, string, int) error { return fail }
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, fail) {
		t.Fatalf("SetSwitch read: %v", err)
	}
	if st, err := s.Status(); err != nil || st.Chat != "off" {
		t.Fatalf("status %+v, %v", st, err)
	}
	// The claim the failed clear left is removed by the next turn-on.
	s.unlink = unix.Unlinkat
	if _, err := s.SetSwitch("read", 1); err != nil {
		t.Fatal(err)
	}
	if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
		t.Fatalf("outbox %q, claims %q", n, c)
	}
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn)
}

// A clear lists the outbox again until a listing finds nothing to remove.
func TestTurnOnClearsUntilEmpty(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	e.reply("a.txt", "Before.")
	added := 0
	s.beforeClaim = func(string) {
		if added < 3 {
			added++
			e.reply(fmt.Sprintf("late%d.txt", added), "During the clear.")
		}
	}
	st, err := s.SetSwitch("read", 1)
	if err != nil || st.Chat != "read" {
		t.Fatalf("SetSwitch: %+v, %v", st, err)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("outbox %q", n)
	}
	if l := e.lines(testRole); !strings.Contains(l[1], `"discarded":4`) {
		t.Fatalf("switch line %s", l[1])
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

func TestParseClaim(t *testing.T) {
	for _, name := range []string{"r1", "x42", "r9007199254740991"} {
		if _, _, ok := parseClaim(name); !ok {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", "r", "r0", "r01", "q1", "r-1", "r1a", "r9007199254740992", "a.txt"} {
		if _, _, ok := parseClaim(name); ok {
			t.Errorf("%q accepted", name)
		}
	}
}
