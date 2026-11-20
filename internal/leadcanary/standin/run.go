//go:build linux && ataniq_canary

package standin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/inject/osc7501"
)

// The names in the replay folder, <home>/canary-replay.
const (
	// ReplayDir is the folder's name under the home.
	ReplayDir = "canary-replay"
	// SwitchFile selects replay mode when it exists in the folder.
	SwitchFile = "replay.on"
	// FirstScreen and SecondScreen are the two screens: the idle screen, and
	// the screen after the paste.
	FirstScreen  = "1.json"
	SecondScreen = "2.json"
	// ReportFile is the optional payload of the idle report.
	ReportFile = "report.txt"
	// OrderFile is what the stand-in writes: which reply came first.
	OrderFile = "order.txt"
	// ResultFile is what replay mode writes after each commit it tries: one
	// line, "committed <short hash>" or "failed: <reason>". The reason is one
	// of a few fixed texts and holds nothing of the message or of git's output.
	ResultFile = "result.txt"
)

// The lines of OrderFile.
const (
	Order7501First   = "7501-first"
	OrderDA1First    = "da1-first"
	OrderNo7501Reply = "no-7501-reply"
)

const (
	// defaultReplyWait is how long replay mode waits for a reply it has not
	// seen: for the reply to the support query after the DA1 reply, and for
	// either reply at start.
	defaultReplyWait = 2 * time.Second
	// defaultReport is the payload of the idle report without ReportFile.
	defaultReport = "state=idle"
	prompt        = "> "
)

// Config is what Run needs. In and Out are the terminal.
type Config struct {
	// Dir is a folder of the working tree. The empty string is ".".
	Dir string
	// Home is the account's home, from HOME. Replay mode is chosen by the
	// switch file in its ReplayDir; an empty Home is plain mode. It is also
	// the HOME of git.
	Home string
	In   io.Reader
	Out  io.Writer
	// TermFD is the descriptor of the terminal to put in raw mode, restored
	// when Run returns; a negative value leaves the terminal alone.
	TermFD int
	// ReplyWait is the time replay mode waits for a reply (zero: 2 s).
	ReplyWait time.Duration
}

// ReplayMode reports whether the switch file is in <home>/canary-replay. The
// file must be a regular file; a link to one does not count.
func ReplayMode(home string) bool {
	if home == "" {
		return false
	}
	st, err := os.Lstat(filepath.Join(home, ReplayDir, SwitchFile))
	return err == nil && st.Mode().IsRegular()
}

// Run runs the stand-in on c.In and c.Out until the input ends or ctx is
// done, and returns nil then. In replay mode it reads its files before it
// touches the terminal, and returns an error, having drawn nothing, when they
// cannot be read; it then removes the OrderFile and ResultFile of an earlier
// run. It restores the terminal on every return.
func Run(ctx context.Context, c Config) error {
	if c.Dir == "" {
		c.Dir = "."
	}
	if c.ReplyWait <= 0 {
		c.ReplyWait = defaultReplyWait
	}
	var rp *replay
	if ReplayMode(c.Home) {
		var err error
		if rp, err = loadReplay(filepath.Join(c.Home, ReplayDir)); err != nil {
			return fmt.Errorf("standin: replay: %w", err)
		}
		if err := rp.removeStale(); err != nil {
			return fmt.Errorf("standin: replay: %w", err)
		}
	}
	if c.TermFD >= 0 {
		restore, err := MakeRaw(c.TermFD)
		if err != nil {
			return fmt.Errorf("standin: terminal: %w", err)
		}
		defer restore()
	}
	l := &loop{c: c, rp: rp}
	return l.run(ctx)
}

// loop is one run of either mode.
type loop struct {
	c  Config
	rp *replay // nil in plain mode
	sc scanner

	// pending is the last paste, until a carriage return submits it.
	pending *pasted
	werr    error
}

// pasted is what is kept of a paste: the token it carried, if any.
type pasted struct {
	token string
	ok    bool
}

// write sends b to the terminal. A write that fails ends the run, as the
// terminal is gone; the first error is kept.
func (l *loop) write(b []byte) {
	if l.werr != nil || len(b) == 0 {
		return
	}
	if _, err := l.c.Out.Write(b); err != nil {
		l.werr = err
	}
}

func (l *loop) run(ctx context.Context) error {
	in := make(chan []byte)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(in)
		buf := make([]byte, 4096)
		for {
			n, err := l.c.In.Read(buf)
			if n > 0 {
				select {
				case in <- append([]byte(nil), buf[:n]...):
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var timer <-chan time.Time
	var t *time.Timer
	setTimer := func() {
		if t == nil {
			t = time.NewTimer(l.c.ReplyWait)
		} else {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(l.c.ReplyWait)
		}
		timer = t.C
	}
	stopTimer := func() {
		if t != nil {
			t.Stop()
		}
		timer = nil
	}
	defer stopTimer()

	if l.rp != nil {
		l.write(l.rp.start())
		setTimer()
	} else {
		l.write([]byte(prompt + seqBracketedPasteOn))
	}
	for l.werr == nil {
		select {
		case <-ctx.Done():
			return nil
		case b, ok := <-in:
			if !ok {
				return nil
			}
			for _, ev := range l.sc.feed(b) {
				if err := l.handle(ev); err != nil {
					return err
				}
				if l.rp != nil && l.rp.decided {
					stopTimer()
				} else if l.rp != nil && l.rp.da1Seen && ev.kind == evReplyDA1 {
					setTimer()
				}
			}
		case <-timer:
			timer = nil
			if l.rp != nil && !l.rp.decided {
				if err := l.rp.decide(OrderNo7501Reply); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// handle acts on one event.
func (l *loop) handle(ev event) error {
	switch ev.kind {
	case evReply7501:
		if l.rp != nil {
			return l.rp.answered(l)
		}
	case evReplyDA1:
		if l.rp != nil {
			l.rp.da1Seen = true
		}
	case evKey:
		if l.rp == nil {
			l.write(echo(ev.data))
		}
	case evPaste:
		tok, ok := ParseToken(string(ev.data))
		l.pending = &pasted{tok, ok}
		if l.rp != nil {
			l.write(l.rp.second())
		} else {
			l.write(echo(ev.data))
		}
	case evCR:
		return l.submit()
	}
	return nil
}

// submit handles a carriage return: a pending paste is a submitted message.
// It returns an error when replay mode cannot write its result.
func (l *loop) submit() error {
	p := l.pending
	l.pending = nil
	if p == nil {
		if l.rp == nil {
			l.write([]byte("\r\n" + prompt))
		}
		return nil
	}
	result := "no token"
	var line string // what replay mode records, when it tried a commit
	if p.ok {
		hash, err := commitTree(l.c.Dir, l.c.Home, p.token)
		if err != nil {
			result = "commit failed: " + string(echo([]byte(err.Error())))
			line = "failed: " + failReason(err)
		} else {
			result = "committed " + Branch
			line = "committed " + hash
		}
	}
	if l.rp != nil {
		if line != "" {
			return l.rp.result(line)
		}
		return nil
	}
	l.write([]byte("\r\n" + result + "\r\n" + prompt))
	return nil
}

// echo makes bytes safe to show: line breaks become CR LF, any other control
// character is shown as ^ and its letter, and what is not valid UTF-8 as "?".
func echo(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			out = append(out, '?')
		case r == '\r' || r == '\n':
			out = append(out, '\r', '\n')
			if r == '\r' && i+1 < len(b) && b[i+1] == '\n' {
				n++
			}
		case r < 0x20:
			out = append(out, '^', byte(r)+0x40)
		case r == 0x7f:
			out = append(out, '^', '?')
		case unicode.IsControl(r):
			out = append(out, '?')
		default:
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}

// replay is replay mode: two screens and an idle report, all from files.
type replay struct {
	dir        string
	one, two   *snapshot
	report     string
	alt        bool // the alternate screen is on
	da1Seen    bool
	decided    bool
	noReply    bool // the order recorded is OrderNo7501Reply
	reportSent bool
}

// loadReplay reads the screens and the report payload from dir.
func loadReplay(dir string) (*replay, error) {
	r := &replay{dir: dir}
	var err error
	if r.one, err = loadSnapshot(filepath.Join(dir, FirstScreen)); err != nil {
		return nil, err
	}
	if r.two, err = loadSnapshot(filepath.Join(dir, SecondScreen)); err != nil {
		return nil, err
	}
	r.report = defaultReport
	switch b, err := readLimited(filepath.Join(dir, ReportFile)); {
	case err == nil:
		r.report = strings.TrimRight(string(b), "\r\n")
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	if err := checkReport(r.report); err != nil {
		return nil, fmt.Errorf("%s: %w", ReportFile, err)
	}
	return r, nil
}

// checkReport accepts a payload that the report parser reads as an idle
// report and that is plain printable text, so that it cannot end its own
// OSC string.
func checkReport(payload string) error {
	for i := 0; i < len(payload); i++ {
		if payload[i] < 0x20 || payload[i] > 0x7e {
			return errors.New("the payload is not printable ASCII")
		}
	}
	rep, err := osc7501.Parse([]byte(payload))
	if err != nil {
		return err
	}
	if rep.State != osc7501.StateIdle {
		return fmt.Errorf("the report's state is %q, not idle", rep.State)
	}
	return nil
}

// start is what replay mode writes first: the two queries and the first
// screen.
func (r *replay) start() []byte {
	b := []byte(osc7501.QueryReply + seqDA1Query)
	b = append(b, r.one.draw(false)...)
	r.alt = r.one.AltScreen
	return b
}

// second is the second screen.
func (r *replay) second() []byte {
	b := r.two.draw(r.alt)
	r.alt = r.two.AltScreen
	return b
}

// answered handles the reply to the support query: it fixes the order, and
// writes the idle report once. A reply that comes after the wait ended, and so
// after "no reply" was recorded, replaces that record with the order it came
// in; "no reply" stands only when no reply ever came.
func (r *replay) answered(l *loop) error {
	if !r.decided || r.noReply {
		order := Order7501First
		if r.da1Seen {
			order = OrderDA1First
		}
		if err := r.decide(order); err != nil {
			return err
		}
	}
	if !r.reportSent {
		r.reportSent = true
		l.write([]byte("\x1b]7501;" + r.report + "\x1b\\"))
	}
	return nil
}

// removeStale removes the OrderFile and ResultFile that an earlier run left in
// the replay folder, so that a file read after this run starts is this run's.
// Each is found by name and not followed: a regular file is removed, a name
// that is not there is skipped, and any other kind of file (a link, a pipe, a
// directory) is left, to be refused when the run writes to it.
func (r *replay) removeStale() error {
	for _, name := range []string{OrderFile, ResultFile} {
		path := filepath.Join(r.dir, name)
		st, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		case st.Mode().IsRegular():
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// decide records the order in OrderFile. It is called once, and again when a
// late reply replaces "no reply".
func (r *replay) decide(order string) error {
	r.decided = true
	r.noReply = order == OrderNo7501Reply
	return writeRegular(filepath.Join(r.dir, OrderFile), order+"\n", 0o600)
}

// result records the outcome of a commit in ResultFile, replacing the line of
// an earlier one.
func (r *replay) result(line string) error {
	return writeRegular(filepath.Join(r.dir, ResultFile), line+"\n", 0o600)
}
