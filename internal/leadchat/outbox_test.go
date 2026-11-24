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

// A folder, a symbolic link, a FIFO, a socket or a device in the outbox is
// never claimed, opened or walked: the chat stops with one entry, and the
// outbox is left as it is.
func TestIngestStopsAtFoldersLinksAndSpecialFiles(t *testing.T) {
	cases := map[string]func(e *env, p string) bool{
		"a folder": func(e *env, p string) bool {
			if err := os.MkdirAll(filepath.Join(p, "sub"), 0o700); err != nil {
				e.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "sub", "x.txt"), []byte("nested"), 0o600); err != nil {
				e.t.Fatal(err)
			}
			return true
		},
		"a dot-folder": func(e *env, p string) bool {
			if err := os.Mkdir(filepath.Join(filepath.Dir(p), ".hidden"), 0o700); err != nil {
				e.t.Fatal(err)
			}
			return true
		},
		"a link": func(e *env, p string) bool {
			if err := os.Symlink(filepath.Join(e.dir, "outside.txt"), p); err != nil {
				e.t.Fatal(err)
			}
			return true
		},
		"a dangling link": func(e *env, p string) bool {
			if err := os.Symlink("/nowhere", p); err != nil {
				e.t.Fatal(err)
			}
			return true
		},
		"a FIFO": func(e *env, p string) bool {
			if err := unix.Mkfifo(p, 0o600); err != nil {
				e.t.Fatal(err)
			}
			return true
		},
		"a socket": func(e *env, p string) bool {
			// A socket is made in a short folder (a socket path is short)
			// and moved in.
			short, err := os.MkdirTemp("", "s")
			if err != nil {
				e.t.Fatal(err)
			}
			defer os.RemoveAll(short)
			fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				e.t.Fatal(err)
			}
			defer unix.Close(fd)
			if err := unix.Bind(fd, &unix.SockaddrUnix{Name: filepath.Join(short, "c")}); err != nil {
				e.t.Fatal(err)
			}
			return os.Rename(filepath.Join(short, "c"), p) == nil
		},
		"a device": func(e *env, p string) bool {
			// A device needs privilege; the case is left out when refused.
			return unix.Mknod(p, unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))) == nil
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.WriteFile(filepath.Join(e.dir, "outside.txt"), []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := e.on()
			e.reply("a.txt", "Never taken.")
			if !mk(e, filepath.Join(e.outbox, "b.txt")) {
				t.Skip("cannot be made here")
			}
			before := e.names()
			rep := ingest(t, s)
			if !rep.Suspended || rep.Taken != 0 {
				t.Fatalf("report %+v", rep)
			}
			if n := e.names(); !slices.Equal(n, before) {
				t.Fatalf("the outbox was touched: %q, was %q", n, before)
			}
			if c := e.claims(); len(c) != 0 {
				t.Fatalf("claim folder %q", c)
			}
			wantTexts(t, s, TextTurnedOn, TextStoppedOdd)
			if _, err := s.Ingest(1); !errors.Is(err, ErrSuspended) {
				t.Fatalf("Ingest while suspended: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(e.dir, "outside.txt")); err != nil || string(data) != "private" {
				t.Fatalf("the link's target changed: %q, %v", data, err)
			}
			if name == "a folder" {
				if _, err := os.Stat(filepath.Join(e.outbox, "b.txt", "sub", "x.txt")); err != nil {
					t.Fatalf("a nested file was touched: %v", err)
				}
			}
		})
	}
}

// A folder or a FIFO that takes a reply's name after the scan judged it and
// before the claim is claimed: it is set aside in the quarantine unopened
// and unwalked, and the chat stops.
func TestIngestSetsAsideWhatTookAReplysNameBeforeTheClaim(t *testing.T) {
	swaps := map[string]func(t *testing.T, p string){
		"a folder": func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Join(p, "deep", "er"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "deep", "x.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/", filepath.Join(p, "root")); err != nil {
				t.Fatal(err)
			}
		},
		"a FIFO": func(t *testing.T, p string) {
			if err := unix.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, swap := range swaps {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := e.on()
			e.reply("a.txt", "Swapped away.")
			e.reply("b.txt", "Not taken.")
			s.beforeClaim = func(name string) {
				s.beforeClaim = nil
				p := filepath.Join(e.outbox, name)
				if err := os.Remove(p); err != nil {
					t.Error(err)
				}
				swap(t, p)
			}
			rep := ingest(t, s)
			if !rep.Suspended || rep.Taken != 0 || len(rep.Dropped) != 0 {
				t.Fatalf("report %+v", rep)
			}
			// 1 is the outbox the turn-on set aside, 2 the claim.
			if n, c, q := e.names(), e.claims(), e.quarantined(); !slices.Equal(n, []string{"b.txt"}) || len(c) != 0 || !slices.Equal(q, []string{"1", "2"}) {
				t.Fatalf("outbox %q, claims %q, quarantine %q", n, c, q)
			}
			if name == "a folder" {
				if _, err := os.Stat(filepath.Join(e.quarDir(), "2", "deep", "x.txt")); err != nil {
					t.Fatalf("the folder set aside was walked: %v", err)
				}
			}
			wantTexts(t, s, TextTurnedOn, TextStoppedOdd)
		})
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
	// a.txt and b.txt have two links when they are judged and are dropped;
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

// A reply that group or other may write, or that its owner may not read, is
// refused; one others may only read is taken: the lead's umask is not the
// daemon's. A reply the daemon cannot open is refused at once: it never
// blocks the replies after it.
func TestIngestReplyModes(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	modes := []struct {
		name string
		mode os.FileMode
	}{{"a.txt", 0o620}, {"b.txt", 0o602}, {"c.txt", 0o644}, {"d.txt", 0o000}, {"e.txt", 0o200}}
	for _, m := range modes {
		e.reply(m.name, "Mode "+m.name+".")
		if err := os.Chmod(filepath.Join(e.outbox, m.name), m.mode); err != nil {
			t.Fatal(err)
		}
	}
	e.reply("f.txt", "After them.")
	rep := ingest(t, s)
	if rep.Dropped[ReasonRefused] != 4 || rep.Taken != 2 || rep.Waiting != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
		t.Fatalf("outbox %q, claims %q", n, c)
	}
	wantTexts(t, s, TextTurnedOn, "Mode c.txt.", "After them.", dropTexts[ReasonRefused])
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

// A lead that keeps a claimed reply open and keeps changing it holds up the
// replies after it only for a bounded time: the claim is dropped after
// UnsettledTries reads that failed, or once UnsettledAge passed since the
// first, and the later replies go on.
func TestIngestBoundsAClaimThatKeepsChanging(t *testing.T) {
	for _, byAge := range []bool{false, true} {
		t.Run(map[bool]string{false: "by tries", true: "by age"}[byAge], func(t *testing.T) {
			e := newEnv(t)
			s := e.on()
			e.reply("a.txt", "Never still")
			// The lead holds its reply open, as a writer that never closed it.
			f, err := os.OpenFile(filepath.Join(e.outbox, "a.txt"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			e.reply("b.txt", "After it.")
			reads := 0
			s.afterRead = func(string) {
				reads++
				if _, err := f.WriteString("."); err != nil {
					t.Error(err)
				}
			}
			scans := UnsettledTries
			if byAge {
				scans = 2
			}
			for i := 1; i < scans; i++ {
				rep := ingest(t, s)
				if rep.Taken != 0 || rep.Waiting != 2 || len(rep.Dropped) != 0 {
					t.Fatalf("scan %d: %+v", i, rep)
				}
				if byAge {
					e.clock.add(UnsettledAge)
				}
			}
			rep := ingest(t, s)
			if rep.Dropped[ReasonUnsettled] != 1 || rep.Taken != 1 || rep.Waiting != 0 {
				t.Fatalf("the last scan: %+v", rep)
			}
			if reads != scans+1 {
				t.Fatalf("%d reads in %d scans", reads, scans)
			}
			if n, c := e.names(), e.claims(); len(n) != 0 || len(c) != 0 {
				t.Fatalf("outbox %q, claims %q", n, c)
			}
			wantTexts(t, s, TextTurnedOn, "After it.", dropTexts[ReasonUnsettled])
		})
	}
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

// Drafts (a name that starts with a dot) and other names are never claimed
// and never removed, however old: they wait, and count toward MaxWaiting.
func TestIngestIgnoresDraftsAndOtherNames(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	others := []string{".draft", "Bad.TXT", "notes", "a.txt.tmp"}
	for _, name := range others {
		e.file(name, "half")
	}
	e.reply("a.txt", "Real reply.")
	rep := ingest(t, s)
	if rep.Taken != 1 || rep.Waiting != len(others) || len(rep.Dropped) != 0 {
		t.Fatalf("report %+v", rep)
	}
	e.clock.add(24 * time.Hour)
	if rep := ingest(t, s); rep.Waiting != len(others) || len(rep.Dropped) != 0 {
		t.Fatalf("a day later: %+v", rep)
	}
	if n := e.names(); !slices.Equal(n, sortedCopy(others)) {
		t.Fatalf("outbox %q", n)
	}
	wantTexts(t, s, TextTurnedOn, "Real reply.")
}

// More than MaxWaiting entries, whatever they are, stop the chat with one
// entry before anything is touched; a fresh turn-on sets them aside.
func TestIngestFloodPastWaiting(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < MaxWaiting-1; i++ {
		switch i % 3 {
		case 0:
			e.reply(fmt.Sprintf("%02d.txt", i), fmt.Sprintf("Reply %d.", i))
		case 1:
			e.file(fmt.Sprintf(".draft%02d", i), "half")
		default:
			e.file(fmt.Sprintf("Bad%02d", i), "bad")
		}
	}
	e.reply("zz.txt", "The last that fits.")
	if n := e.names(); len(n) != MaxWaiting {
		t.Fatalf("%d entries", len(n))
	}
	e.reply("zzz.txt", "One too many.")
	rep := ingest(t, s)
	if !rep.Suspended || rep.Taken != 0 || len(rep.Dropped) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if n := e.names(); len(n) != MaxWaiting+1 {
		t.Fatalf("the outbox was touched: %d entries", len(n))
	}
	wantTexts(t, s, TextTurnedOn, TextStoppedFull)
	if _, err := s.Ingest(1); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Ingest while suspended: %v", err)
	}
	if st, _ := s.Status(); st.Chat != "read" || !st.Suspended {
		t.Fatalf("status %+v", st)
	}
	st, err := s.SetSwitch("read", 2)
	if err != nil || st.Suspended || st.Gen != 3 {
		t.Fatalf("a fresh turn-on: %+v, %v", st, err)
	}
	if n, q := e.names(), e.quarantined(); len(n) != 0 || !slices.Equal(q, []string{"1", "2"}) {
		t.Fatalf("the fresh turn-on left outbox %q, quarantine %q", n, q)
	}
	if old := e.list(filepath.Join(e.quarDir(), "2")); len(old) != MaxWaiting+1 {
		t.Fatalf("%d entries set aside", len(old))
	}
	wantTexts(t, s, TextTurnedOn)
}

// A claim left unread counts toward MaxWaiting, and the count is taken first:
// an outbox of MaxWaiting entries with one claim unread stops the chat before
// the claim is read again.
func TestIngestFullCountsAClaimLeftUnread(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Changing")
	reads := 0
	s.afterRead = func(name string) {
		reads++
		f, err := os.OpenFile(filepath.Join(e.claimDir(), name), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = f.WriteString(".")
		f.Close()
	}
	if rep := ingest(t, s); rep.Waiting != 1 || reads != 1 {
		t.Fatalf("report %+v, %d reads", rep, reads)
	}
	for i := 0; i < MaxWaiting; i++ {
		e.file(fmt.Sprintf(".draft%02d", i), "half")
	}
	rep := ingest(t, s)
	if !rep.Suspended || rep.Taken != 0 || len(rep.Dropped) != 0 || reads != 1 {
		t.Fatalf("report %+v, %d reads", rep, reads)
	}
	if c := e.claims(); !slices.Equal(c, []string{"r1"}) {
		t.Fatalf("claims %q", c)
	}
	wantTexts(t, s, TextTurnedOn, TextStoppedFull)
	// The fresh turn-on discards the claim and sets the outbox aside.
	if _, err := s.SetSwitch("read", 1); err != nil {
		t.Fatal(err)
	}
	if c, n := e.claims(), e.names(); len(c) != 0 || len(n) != 0 {
		t.Fatalf("claims %q, outbox %q", c, n)
	}
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

// The crash points of a reply within a segment: after its claim, after its
// entry is stored, and a removal of its claim that fails. A retry in the
// same segment commits each claim at most once.
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
}

// A restart ends the segment: every lead opens off, and the next turn-on
// discards the claims and the outbox the segment left. Nothing of the
// earlier segment is shown again, and nothing is stored twice.
func TestRestartDiscardsTheSegment(t *testing.T) {
	crash := errors.New("synthetic crash")
	t.Run("before the append", func(t *testing.T) {
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
		if n, c, q := e.names(), e.claims(), e.quarantined(); len(n) != 0 || len(c) != 0 || !slices.Equal(q, []string{"1", "2"}) {
			t.Fatalf("the turn-on left outbox %q, claims %q, quarantine %q", n, c, q)
		}
		if l := e.lines(testRole); !strings.Contains(l[len(l)-2], `"discarded":2`) {
			t.Fatalf("switch line %s", l[len(l)-2])
		}
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn)
	})
	t.Run("after the append, before the claim's removal", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Stored once.")
		s.afterAppend = func() error { return crash }
		if _, err := s.Ingest(1); !errors.Is(err, crash) {
			t.Fatalf("Ingest: %v", err)
		}
		if l := strings.Join(e.lines(testRole), "\n"); strings.Count(l, "Stored once.") != 1 {
			t.Fatalf("the entry was not synced: %s", l)
		}
		if c := e.claims(); !slices.Equal(c, []string{"r1"}) {
			t.Fatalf("claims %q", c)
		}
		_ = s.Close()
		s = e.open()
		if st, _ := s.Status(); st.Chat != "off" || st.Entries != 0 || st.Head != 2 {
			t.Fatalf("status after restart %+v", st)
		}
		if l := strings.Join(e.lines(testRole), "\n"); strings.Contains(l, "Stored once.") {
			t.Fatalf("the restart kept the entry: %s", l)
		}
		if _, err := s.SetSwitch("read", 2); err != nil {
			t.Fatal(err)
		}
		if c := e.claims(); len(c) != 0 {
			t.Fatalf("the turn-on left claims %q", c)
		}
		rep := ingest(t, s)
		if rep.Taken != 0 || rep.Repaired != 0 {
			t.Fatalf("report %+v", rep)
		}
		es := read(t, s)
		if !slices.Equal(texts(es), []string{TextTurnedOn}) || es[0].Seq != 3 {
			t.Fatalf("entries %+v", es)
		}
		if l := strings.Join(e.lines(testRole), "\n"); strings.Contains(l, "Stored once.") {
			t.Fatalf("the old entry came back: %s", l)
		}
	})
}

// The outbox's name must still lead to the folder the turn-on made and
// holds: a replaced, removed or opened outbox stops the chat with one entry
// until a fresh turn-on.
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
		"removed and made again": func(e *env) {
			if err := os.Remove(e.outbox); err != nil {
				e.t.Fatal(err)
			}
			e.makeOutbox(testRole)
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
	t.Run("a fresh turn-on makes a new folder", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		cases["replaced"](e)
		if rep := ingest(t, s); !rep.Suspended {
			t.Fatalf("report %+v", rep)
		}
		e.reply("b.txt", "Into the lead's own folder.")
		st, err := s.SetSwitch("read", 1)
		if err != nil || st.Suspended || st.Chat != "read" {
			t.Fatalf("SetSwitch: %+v, %v", st, err)
		}
		e.reply("c.txt", "After the fresh turn-on.")
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn, "After the fresh turn-on.")
	})
	t.Run("no openat2 after the turn-on", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.root.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
		if rep := ingest(t, s); !rep.Suspended {
			t.Fatalf("report %+v", rep)
		}
		wantTexts(t, s, TextTurnedOn, TextStoppedFolder)
	})
	t.Run("no rename after the turn-on", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		e.reply("a.txt", "Cannot be claimed.")
		s.rename = func(int, string, int, string, uint) error { return unix.EOPNOTSUPP }
		if rep := ingest(t, s); !rep.Suspended {
			t.Fatalf("report %+v", rep)
		}
		wantTexts(t, s, TextTurnedOn, TextStoppedFolder)
	})
}

// Every turn-on makes the outbox afresh: an earlier, empty one is removed,
// and an earlier one that holds anything, or is not a folder, is set aside
// whole, unread, in the quarantine.
func TestTurnOnMakesAFreshOutbox(t *testing.T) {
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
		if q := e.quarantined(); len(q) != 0 {
			t.Fatalf("quarantine %q", q)
		}
	})
	t.Run("an empty one is set aside", func(t *testing.T) {
		e := newEnv(t)
		var before unix.Stat_t
		if err := unix.Stat(e.outbox, &before); err != nil {
			t.Fatal(err)
		}
		s := e.on()
		if q := e.quarantined(); !slices.Equal(q, []string{"1"}) {
			t.Fatalf("quarantine %q", q)
		}
		var aside unix.Stat_t
		if err := unix.Stat(filepath.Join(e.quarDir(), "1"), &aside); err != nil || aside.Ino != before.Ino {
			t.Fatalf("the earlier, empty outbox was not set aside: %v", err)
		}
		if l := e.lines(testRole); !strings.Contains(l[1], `"discarded":1`) {
			t.Fatalf("switch line %s", l[1])
		}
		for _, sw := range []string{"off", "read", "off", "read"} {
			if _, err := s.SetSwitch(sw, 1); err != nil {
				t.Fatal(err)
			}
		}
		if q := e.quarantined(); !slices.Equal(q, []string{"1", "2", "3"}) {
			t.Fatalf("quarantine %q", q)
		}
		if l := e.lines(testRole); !strings.Contains(l[len(l)-2], `"discarded":1`) {
			t.Fatalf("switch line %s", l[len(l)-2])
		}
		// A full quarantine of empty folders frees a name by removing one:
		// nothing set aside is lost.
		for i := 0; i < 3; i++ {
			if _, err := s.SetSwitch("off", 1); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetSwitch("read", 1); err != nil {
				t.Fatalf("turn-on with a quarantine of empty folders: %v", err)
			}
		}
		if q := e.quarantined(); len(q) != MaxQuarantine {
			t.Fatalf("quarantine %q", q)
		}
	})
	t.Run("one that holds anything is set aside", func(t *testing.T) {
		e := newEnv(t)
		if err := os.WriteFile(filepath.Join(e.dir, "outside.txt"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		e.reply("old.txt", "From before.")
		e.file(".draft", "half")
		if err := os.MkdirAll(filepath.Join(e.outbox, "sub", "deeper"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(e.dir, "outside.txt"), filepath.Join(e.outbox, "link.txt")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(e.outbox, "fifo.txt"), 0o600); err != nil {
			t.Fatal(err)
		}
		var before unix.Stat_t
		if err := unix.Stat(e.outbox, &before); err != nil {
			t.Fatal(err)
		}
		s := e.on()
		var after unix.Stat_t
		if err := unix.Stat(e.outbox, &after); err != nil {
			t.Fatal(err)
		}
		if after.Ino == before.Ino || after.Mode&0o7777 != 0o700 {
			t.Fatalf("the outbox is not fresh: inode %d, was %d, mode %o", after.Ino, before.Ino, after.Mode&0o7777)
		}
		if n, q := e.names(), e.quarantined(); len(n) != 0 || !slices.Equal(q, []string{"1"}) {
			t.Fatalf("outbox %q, quarantine %q", n, q)
		}
		if old := e.list(filepath.Join(e.quarDir(), "1")); !slices.Equal(old, []string{".draft", "fifo.txt", "link.txt", "old.txt", "sub"}) {
			t.Fatalf("set aside %q", old)
		}
		if fi, err := os.Lstat(e.quarDir()); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("quarantine %v, %v", fi, err)
		}
		if l := e.lines(testRole); !strings.Contains(l[1], `"discarded":1`) {
			t.Fatalf("switch line %s", l[1])
		}
		if data, err := os.ReadFile(filepath.Join(e.dir, "outside.txt")); err != nil || string(data) != "private" {
			t.Fatalf("the link's target changed: %q, %v", data, err)
		}
		e.reply("new.txt", "After.")
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn, "After.")
	})
	t.Run("one that is not a folder is set aside", func(t *testing.T) {
		for name, mk := range map[string]func(e *env){
			"a link": func(e *env) {
				elsewhere := filepath.Join(e.dir, "elsewhere")
				if err := os.Mkdir(elsewhere, 0o700); err != nil {
					e.t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(elsewhere, "a.txt"), []byte("Planted."), 0o600); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, e.outbox); err != nil {
					e.t.Fatal(err)
				}
			},
			"a file": func(e *env) {
				if err := os.WriteFile(e.outbox, []byte("not a folder"), 0o600); err != nil {
					e.t.Fatal(err)
				}
			},
			"a folder open to others": func(e *env) {
				if err := os.Mkdir(e.outbox, 0o777); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Chmod(e.outbox, 0o777); err != nil {
					e.t.Fatal(err)
				}
				e.file("a.txt", "Planted.")
			},
		} {
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				if err := os.Remove(e.outbox); err != nil {
					t.Fatal(err)
				}
				mk(e)
				s := e.on()
				if fi, err := os.Lstat(e.outbox); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
					t.Fatalf("outbox %v, %v", fi, err)
				}
				if q := e.quarantined(); !slices.Equal(q, []string{"1"}) {
					t.Fatalf("quarantine %q", q)
				}
				if _, err := os.Stat(filepath.Join(e.dir, "elsewhere", "a.txt")); name == "a link" && err != nil {
					t.Fatalf("the planted file was touched: %v", err)
				}
				ingest(t, s)
				wantTexts(t, s, TextTurnedOn)
			})
		}
	})
	t.Run("made again by the lead during the turn-on", func(t *testing.T) {
		e := newEnv(t)
		s := e.open()
		s.beforePublish = func() {
			e.makeOutbox(testRole)
			e.file("planted.txt", "Made before the daemon's folder was published.")
		}
		if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("SetSwitch read: %v", err)
		}
		if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
			t.Fatalf("status %+v, %v", st, err)
		}
		// The staged folder went to the quarantine, and the lead's folder
		// was not pinned.
		if q := e.quarantined(); !slices.Equal(q, []string{"1", "2"}) {
			t.Fatalf("quarantine %q", q)
		}
		if n := e.list(filepath.Join(e.dir, "leads", testRole, StagingName)); len(n) != 0 {
			t.Fatalf("staging %q", n)
		}
		s.beforePublish = nil
		if _, err := s.SetSwitch("read", 1); err != nil {
			t.Fatal(err)
		}
		if n := e.names(); len(n) != 0 {
			t.Fatalf("the published outbox holds %q", n)
		}
		e.reply("a.txt", "After.")
		ingest(t, s)
		wantTexts(t, s, TextTurnedOn, "After.")
	})
	t.Run("replaced in the staging folder before it is opened", func(t *testing.T) {
		for name, swap := range map[string]func(e *env, staged string){
			"a folder that holds a reply": func(e *env, staged string) {
				if err := os.Rename(staged, staged+".made"); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Mkdir(staged, 0o700); err != nil {
					e.t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(staged, "old.txt"), []byte("From before."), 0o600); err != nil {
					e.t.Fatal(err)
				}
			},
			"a link": func(e *env, staged string) {
				elsewhere := filepath.Join(e.dir, "elsewhere")
				if err := os.Mkdir(elsewhere, 0o700); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Remove(staged); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, staged); err != nil {
					e.t.Fatal(err)
				}
			},
			"gone": func(e *env, staged string) {
				if err := os.Remove(staged); err != nil {
					e.t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				s := e.open()
				staged := filepath.Join(e.dir, "leads", testRole, StagingName, stagedName)
				s.afterStage = func() { swap(e, staged) }
				if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("SetSwitch read: %v", err)
				}
				if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
					t.Fatalf("status %+v, %v", st, err)
				}
				// Nothing was published: the earlier outbox is still at
				// its name, untouched.
				if fi, err := os.Lstat(e.outbox); err != nil || !fi.IsDir() {
					t.Fatalf("outbox %v, %v", fi, err)
				}
				if n := e.names(); len(n) != 0 {
					t.Fatalf("the outbox holds %q", n)
				}
				s.afterStage = nil
				if _, err := s.SetSwitch("read", 1); err != nil {
					t.Fatal(err)
				}
				ingest(t, s)
				wantTexts(t, s, TextTurnedOn)
			})
		}
	})
	t.Run("replaced right after it is published", func(t *testing.T) {
		e := newEnv(t)
		s := e.open()
		s.afterPublish = func() {
			if err := os.Rename(e.outbox, e.outbox+".moved"); err != nil {
				e.t.Fatal(err)
			}
			e.makeOutbox(testRole)
			e.file("planted.txt", "Not the daemon's folder.")
		}
		if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("SetSwitch read: %v", err)
		}
		if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
			t.Fatalf("status %+v, %v", st, err)
		}
		if _, err := os.Lstat(e.outbox); !os.IsNotExist(err) {
			t.Fatalf("the replacement was left at the outbox's name: %v", err)
		}
		if q := e.quarantined(); !slices.Equal(q, []string{"1", "2"}) {
			t.Fatalf("quarantine %q", q)
		}
		if n := e.list(filepath.Join(e.quarDir(), "2")); !slices.Equal(n, []string{"planted.txt"}) {
			t.Fatalf("set aside %q", n)
		}
	})
}

// The quarantine holds MaxQuarantine entries: a turn-on that would set aside
// one more, when none of them is an empty folder, is refused until the owner
// purges it.
func TestQuarantineIsBounded(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	for i := 1; i <= MaxQuarantine; i++ {
		e.file("left.txt", "Left behind.")
		if _, err := s.SetSwitch("read", 1); err != nil {
			t.Fatalf("turn-on %d: %v", i, err)
		}
		if _, err := s.SetSwitch("off", 1); err != nil {
			t.Fatal(err)
		}
	}
	if q := e.quarantined(); !slices.Equal(q, []string{"1", "2", "3", "4"}) {
		t.Fatalf("quarantine %q", q)
	}
	e.file("left.txt", "Left behind.")
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("SetSwitch read: %v", err)
	}
	if st, err := s.Status(); err != nil || st.Chat != "off" {
		t.Fatalf("status %+v, %v", st, err)
	}
	if n := e.names(); !slices.Equal(n, []string{"left.txt"}) {
		t.Fatalf("outbox %q", n)
	}
	rep, err := s.PurgeQuarantine()
	if err != nil || rep.Removed != MaxQuarantine || rep.Left != 0 {
		t.Fatalf("PurgeQuarantine: %+v, %v", rep, err)
	}
	if _, err := s.SetSwitch("read", 1); err != nil {
		t.Fatal(err)
	}
	// 1 is the folder the refused turn-on made and could not set aside, 2
	// the earlier outbox.
	if q := e.quarantined(); !slices.Equal(q, []string{"1", "2"}) {
		t.Fatalf("quarantine %q", q)
	}
	if n := e.list(filepath.Join(e.quarDir(), "2")); !slices.Equal(n, []string{"left.txt"}) {
		t.Fatalf("set aside %q", n)
	}
}

// A purge removes what it can within its bounds, never follows a link, and
// leaves and counts what it cannot.
func TestPurgeQuarantine(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if rep, err := s.PurgeQuarantine(); err != nil || rep != (PurgeReport{}) {
		t.Fatalf("no quarantine: %+v, %v", rep, err)
	}
	outside := filepath.Join(e.dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.quarDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// 1: a shallow tree with links out; 2: deeper than a purge goes;
	// 3: a file; 4: a folder closed to the daemon.
	one := filepath.Join(e.quarDir(), "1")
	if err := os.MkdirAll(filepath.Join(one, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(one, "a", "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(one, "a", "b", "keep")); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(e.quarDir(), "2")
	for i := 0; i < maxTreeDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.quarDir(), "3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(e.quarDir(), "4")
	if err := os.MkdirAll(filepath.Join(closed, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	rep, err := s.PurgeQuarantine()
	if err != nil || rep.Removed != 2 || rep.Left != 2 {
		t.Fatalf("PurgeQuarantine: %+v, %v", rep, err)
	}
	if q := e.quarantined(); !slices.Equal(q, []string{"2", "4"}) {
		t.Fatalf("quarantine %q", q)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("a link was followed: %q, %v", data, err)
	}
}

// A turn-on needs the lead's home reached safely and a file system that
// claims: otherwise it is refused and the switch stays off.
func TestTurnOnRefuses(t *testing.T) {
	enosys := func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
	eperm := func(int, string, *unix.OpenHow) (int, error) { return -1, unix.EPERM }
	renameFails := func(errno unix.Errno) func(e *env, s *Store) {
		return func(e *env, s *Store) {
			s.rename = func(int, string, int, string, uint) error { return errno }
		}
	}
	refused := map[string]struct {
		change func(e *env, s *Store)
		want   error
	}{
		"the home a link": {func(e *env, s *Store) {
			home := filepath.Dir(e.outbox)
			elsewhere := filepath.Join(e.dir, "elsewhere")
			if err := os.Rename(home, elsewhere); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, home); err != nil {
				e.t.Fatal(err)
			}
		}, ErrInsecure},
		"the lead's folder open to others": {func(e *env, s *Store) {
			if err := os.Chmod(filepath.Join(e.dir, "leads", testRole), 0o777); err != nil {
				e.t.Fatal(err)
			}
		}, ErrInsecure},
		"no home": {func(e *env, s *Store) {
			if err := os.RemoveAll(filepath.Dir(e.outbox)); err != nil {
				e.t.Fatal(err)
			}
		}, ErrUnavailable},
		"no openat2":            {func(e *env, s *Store) { e.root.openat2 = enosys }, ErrUnavailable},
		"openat2 denied":        {func(e *env, s *Store) { e.root.openat2 = eperm }, ErrUnavailable},
		"rename not supported":  {renameFails(unix.EOPNOTSUPP), ErrUnavailable},
		"rename flag not known": {renameFails(unix.EINVAL), ErrUnavailable},
		"rename denied":         {renameFails(unix.EPERM), ErrUnavailable},
		"rename refused":        {renameFails(unix.EACCES), ErrUnavailable},
		"no renameat2":          {renameFails(unix.ENOSYS), ErrUnavailable},
		"two file systems":      {renameFails(unix.EXDEV), ErrUnavailable},
		"the probe's removal denied": {func(e *env, s *Store) {
			s.unlink = func(dirfd int, name string, flags int) error {
				if name == probeClaim {
					return unix.EPERM
				}
				return unix.Unlinkat(dirfd, name, flags)
			}
		}, ErrUnavailable},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := e.open()
			c.change(e, s)
			if _, err := s.SetSwitch("read", 1); !errors.Is(err, c.want) {
				t.Fatalf("SetSwitch read: %v, want %v", err, c.want)
			}
			if st, err := s.Status(); err != nil || st.Chat != "off" || st.Gen != 0 {
				t.Fatalf("status %+v, %v", st, err)
			}
		})
	}
	// A refused probe leaves nothing that takes a place in the quarantine.
	t.Run("a probe refused, then allowed", func(t *testing.T) {
		e := newEnv(t)
		s := e.open()
		s.rename = func(int, string, int, string, uint) error { return unix.EOPNOTSUPP }
		for i := 0; i < MaxQuarantine+1; i++ {
			if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("SetSwitch read: %v", err)
			}
		}
		s.rename = unix.Renameat2
		if _, err := s.SetSwitch("read", 1); err != nil {
			t.Fatal(err)
		}
		// The quarantine holds the folder the refused turn-ons made and the
		// earlier outbox, both empty: a name an empty folder holds is freed
		// when it is needed.
		q, c := e.quarantined(), e.claims()
		if len(c) != 0 || !slices.Equal(q, []string{"1", "2"}) {
			t.Fatalf("quarantine %q, claims %q", q, c)
		}
		for _, name := range q {
			if n := e.list(filepath.Join(e.quarDir(), name)); len(n) != 0 {
				t.Fatalf("quarantine %s holds %q", name, n)
			}
		}
	})
}

// The claims an earlier segment left are discarded by the next turn-on: a
// file is removed unopened, a folder set aside unwalked; a removal that
// fails refuses the turn-on.
func TestTurnOnDiscardsEarlierClaims(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if err := os.WriteFile(filepath.Join(e.claimDir(), "r7"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.claimDir(), "r8", "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("synthetic removal failure")
	s.unlink = func(int, string, int) error { return fail }
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, fail) {
		t.Fatalf("SetSwitch read: %v", err)
	}
	if st, err := s.Status(); err != nil || st.Chat != "off" {
		t.Fatalf("status %+v, %v", st, err)
	}
	s.unlink = unix.Unlinkat
	if _, err := s.SetSwitch("read", 1); err != nil {
		t.Fatal(err)
	}
	if c, q := e.claims(), e.quarantined(); len(c) != 0 || !slices.Equal(q, []string{"1", "2"}) {
		t.Fatalf("claims %q, quarantine %q", c, q)
	}
	if _, err := os.Stat(filepath.Join(e.quarDir(), "1", "inner")); err != nil {
		t.Fatalf("the folder set aside: %v", err)
	}
	if l := e.lines(testRole); !strings.Contains(l[1], `"discarded":3`) {
		t.Fatalf("switch line %s", l[1])
	}
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn)
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
