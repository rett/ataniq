//go:build linux

// Package fakeagent is a scripted stand-in for an agent, for the tests of the
// lead holder. No real agent is ever started by a test. It needs no separate
// binary: a test binary re-executes itself as the fake agent.
//
// In TestMain call RunIfChild first. Then build the child with Command:
//
//	func TestMain(m *testing.M) {
//		fakeagent.RunIfChild()
//		os.Exit(m.Run())
//	}
//
//	argv, env := fakeagent.Command("raw; say READY; echo 5; exit 3")
//
// argv is the path of the running executable; env holds the one marker
// variable, EnvScript, that carries the script. Add further entries to env
// as the test needs (the child's environment is exactly what its starter
// gives it). Main runs a script from EnvScript or, when that is unset, from
// the command-line arguments joined by spaces, so a thin main package can
// serve as a real binary later.
//
// # Script format
//
// A script is steps separated by ";". A step is a verb and its arguments
// separated by white space, so an argument cannot hold white space or ";"
// (paths under a test's temporary directory are fine). Steps run in order;
// when the last one returns the agent exits 0. A script error, found before
// anything runs when the verb or the argument count is wrong, exits 64; a
// failing step exits 70, with the reason on stderr.
//
// Output (all to the terminal on stdout):
//
//	print HEX             write the bytes given in hex
//	printfile PATH        write the file's bytes (at most 1 MiB)
//	say WORD              write WORD and CR LF
//	flood RATE TOTAL SEED write TOTAL bytes (0: until killed) at about RATE
//	                      bytes a second (0: unpaced), the same bytes for the
//	                      same SEED (see FloodBytes)
//	size                  write "SIZE COLSxROWS"
//	watchwinch            from now on write "WINCH COLSxROWS" on every SIGWINCH
//
// Modes (DEC private modes, CSI ? n h and l; a name or a number):
//
//	mode NAME...          set: mouse1000 mouse1002 mouse1003 mouse1006
//	                      paste (2004) focus (1004) alt (1049) sync (2026)
//	unmode NAME...        reset
//	frame HEX             a synchronized-output frame: set 2026, write the
//	                      bytes, reset 2026
//	frameopen HEX         set 2026, write the bytes, and leave the frame open
//
// Input:
//
//	raw                   raw mode (no echo, no ISIG, no output processing)
//	cooked                cooked mode (ICANON, ECHO, ISIG, ICRNL, OPOST)
//	isig on|off           set or clear only ISIG
//	echo [N]              read input; for each read write "RX:" and the hex of
//	                      the bytes and CR LF; with N, go on after N bytes
//	query LIST FILE       in raw mode for the time of the step, send the
//	                      queries in LIST (comma separated: osc10 osc11 osc12
//	                      da1), read the reply until the device attributes
//	                      reply (when da1 is asked) or 1 s of quiet, and write
//	                      the bytes it read to FILE
//
// Timing and ending:
//
//	sleep DURATION        a Go duration, such as 50ms
//	wait                  never returns (until killed)
//	exit CODE             exit with the status
//	die SIGNAL            die from the signal: TERM KILL HUP INT USR1 USR2 ALRM
//	ignore SIGNAL[,...]   ignore signals (the same names)
//
// Processes and facts:
//
//	spawn KIND FILE [detach] [ttl=DURATION]
//	                      start a copy of this agent that ignores SIGHUP, writes
//	                      its pid to FILE and sleeps for the ttl (default 60s);
//	                      the step returns when FILE exists. KIND is
//	                      same (same process group), group (own group, same
//	                      session) or session (own session, no controlling
//	                      terminal). It keeps the terminal as stdin, stdout and
//	                      stderr unless detach is given (then /dev/null).
//	pidfile FILE          write this process's pid to FILE
//	report FILE           write facts to FILE: session id, controlling
//	                      terminal, whether stdin is a terminal, the
//	                      environment (see Report and ReadReport)
//
// Files are written whole and then renamed into place, so a test that polls
// for a file reads a complete one.
package fakeagent

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// EnvScript is the environment variable that carries the script.
const EnvScript = "ATANIQ_FAKEAGENT_SCRIPT"

// exit ends the process at once, without the runtime's exit hooks: the race
// detector would otherwise hold every exiting agent for a second.
func exit(code int) { syscall.Exit(code) }

const (
	exitScript = 64 // a script error
	exitStep   = 70 // a step failed

	maxPrintFile = 1 << 20
	maxQueryRead = 64 << 10
	floodChunk   = 32 << 10
)

// Command returns the argv and env that run script as the fake agent: the
// running executable, and a marker variable that RunIfChild reacts to.
func Command(script string) (argv, env []string) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return []string{exe}, []string{EnvScript + "=" + script}
}

// RunIfChild runs the fake agent and exits when EnvScript is in the
// environment, and returns at once when it is not. Call it first in
// TestMain.
func RunIfChild() {
	if _, ok := os.LookupEnv(EnvScript); ok {
		Main()
	}
}

// Main runs the script of EnvScript, or of the arguments when that is
// unset, and exits with the script's status. It does not return.
func Main() {
	script, ok := os.LookupEnv(EnvScript)
	if !ok {
		script = strings.Join(os.Args[1:], " ")
	}
	exit(Run(script))
}

// Run runs a script on this process's stdin, stdout and stderr and returns
// the exit status; the "exit" and "die" steps end the process instead.
func Run(script string) int {
	steps, err := parse(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return exitScript
	}
	a := &agent{}
	for _, s := range steps {
		if err := s.verb.run(a, s.args); err != nil {
			fmt.Fprintf(os.Stderr, "fakeagent: %s: %v\n", s.name, err)
			return exitStep
		}
	}
	return 0
}

type verb struct {
	min, max int // argument count, max < 0: no limit
	run      func(a *agent, args []string) error
}

type step struct {
	name string
	args []string
	verb verb
}

func parse(script string) ([]step, error) {
	var steps []step
	for _, raw := range strings.Split(script, ";") {
		f := strings.Fields(raw)
		if len(f) == 0 {
			continue
		}
		v, ok := verbs[f[0]]
		if !ok {
			return nil, fmt.Errorf("unknown verb %q", f[0])
		}
		if n := len(f) - 1; n < v.min || (v.max >= 0 && n > v.max) {
			return nil, fmt.Errorf("%s: %d arguments, want %d..%d", f[0], n, v.min, v.max)
		}
		steps = append(steps, step{name: f[0], args: f[1:], verb: v})
	}
	return steps, nil
}

var verbs map[string]verb

func init() {
	verbs = map[string]verb{
		"print":      {1, 1, (*agent).print},
		"printfile":  {1, 1, (*agent).printFile},
		"say":        {1, 1, (*agent).say},
		"flood":      {3, 3, (*agent).flood},
		"size":       {0, 0, (*agent).size},
		"watchwinch": {0, 0, (*agent).watchWinch},
		"mode":       {1, -1, func(a *agent, args []string) error { return a.mode(args, 'h') }},
		"unmode":     {1, -1, func(a *agent, args []string) error { return a.mode(args, 'l') }},
		"frame":      {0, 1, func(a *agent, args []string) error { return a.frame(args, true) }},
		"frameopen":  {0, 1, func(a *agent, args []string) error { return a.frame(args, false) }},
		"raw":        {0, 0, func(a *agent, _ []string) error { return setTermios(makeRaw) }},
		"cooked":     {0, 0, func(a *agent, _ []string) error { return setTermios(makeCooked) }},
		"isig":       {1, 1, (*agent).isig},
		"echo":       {0, 1, (*agent).echo},
		"query":      {2, 2, (*agent).query},
		"sleep":      {1, 1, (*agent).sleep},
		"wait": {0, 0, func(*agent, []string) error {
			for {
				time.Sleep(time.Hour)
			}
		}},
		"exit":    {1, 1, (*agent).exitStep},
		"die":     {1, 1, (*agent).die},
		"ignore":  {1, 1, (*agent).ignore},
		"spawn":   {2, 4, (*agent).spawn},
		"pidfile": {1, 1, (*agent).pidfile},
		"report":  {1, 1, (*agent).report},
	}
}

type agent struct {
	outMu sync.Mutex
}

// out writes to the terminal, whole, one writer at a time.
func (a *agent) out(b []byte) error {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	for len(b) > 0 {
		n, err := os.Stdout.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (a *agent) print(args []string) error {
	b, err := hex.DecodeString(strings.TrimPrefix(args[0], "0x"))
	if err != nil {
		return err
	}
	return a.out(b)
}

func (a *agent) printFile(args []string) error {
	b, err := readLimited(args[0], maxPrintFile)
	if err != nil {
		return err
	}
	return a.out(b)
}

func (a *agent) say(args []string) error { return a.out([]byte(args[0] + "\r\n")) }

func (a *agent) sleep(args []string) error {
	d, err := time.ParseDuration(args[0])
	if err != nil {
		return err
	}
	time.Sleep(d)
	return nil
}

func (a *agent) exitStep(args []string) error {
	code, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}
	exit(code)
	return nil
}

var signals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP, "INT": syscall.SIGINT,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "ALRM": syscall.SIGALRM,
}

func sigByName(n string) (syscall.Signal, bool) {
	sig, ok := signals[strings.TrimPrefix(strings.ToUpper(n), "SIG")]
	return sig, ok
}

func (a *agent) die(args []string) error {
	sig, ok := sigByName(args[0])
	if !ok {
		return fmt.Errorf("unknown signal %q", args[0])
	}
	signal.Reset(sig)
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		return err
	}
	time.Sleep(time.Minute) // the signal ends this; a stray survival is a failure
	return errors.New("survived its own signal")
}

func (a *agent) ignore(args []string) error {
	for _, n := range strings.Split(args[0], ",") {
		sig, ok := sigByName(n)
		if !ok || sig == syscall.SIGKILL {
			return fmt.Errorf("cannot ignore %q", n)
		}
		signal.Ignore(sig)
	}
	return nil
}

var modeNames = map[string]int{
	"mouse1000": 1000, "mouse1002": 1002, "mouse1003": 1003, "mouse1006": 1006,
	"paste": 2004, "focus": 1004, "alt": 1049, "sync": 2026,
}

func (a *agent) mode(args []string, final byte) error {
	var b []byte
	for _, n := range args {
		num, ok := modeNames[n]
		if !ok {
			var err error
			if num, err = strconv.Atoi(n); err != nil || num <= 0 {
				return fmt.Errorf("unknown mode %q", n)
			}
		}
		b = append(b, fmt.Sprintf("\x1b[?%d%c", num, final)...)
	}
	return a.out(b)
}

func (a *agent) frame(args []string, closed bool) error {
	var body []byte
	if len(args) == 1 {
		var err error
		if body, err = hex.DecodeString(args[0]); err != nil {
			return err
		}
	}
	b := append([]byte("\x1b[?2026h"), body...)
	if closed {
		b = append(b, "\x1b[?2026l"...)
	}
	return a.out(b)
}

func (a *agent) winsize() (cols, rows int, err error) {
	ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

func (a *agent) size([]string) error {
	c, r, err := a.winsize()
	if err != nil {
		return err
	}
	return a.out([]byte(fmt.Sprintf("SIZE %dx%d\r\n", c, r)))
}

func (a *agent) watchWinch([]string) error {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if c, r, err := a.winsize(); err == nil {
				_ = a.out([]byte(fmt.Sprintf("WINCH %dx%d\r\n", c, r)))
			}
		}
	}()
	return nil
}

func (a *agent) echo(args []string) error {
	limit := -1
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			return fmt.Errorf("bad byte count %q", args[0])
		}
		limit = n
	}
	buf := make([]byte, 4096)
	got := 0
	for limit < 0 || got < limit {
		n, err := unix.Read(0, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return nil // the input ended
		}
		got += n
		if err := a.out([]byte("RX:" + hex.EncodeToString(buf[:n]) + "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

func (a *agent) isig(args []string) error {
	switch args[0] {
	case "on":
		return setTermios(func(t *unix.Termios) { t.Lflag |= unix.ISIG })
	case "off":
		return setTermios(func(t *unix.Termios) { t.Lflag &^= unix.ISIG })
	}
	return fmt.Errorf("isig wants on or off, not %q", args[0])
}

func setTermios(f func(*unix.Termios)) error {
	t, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return err
	}
	f(t)
	return unix.IoctlSetTermios(0, unix.TCSETS, t)
}

func makeRaw(t *unix.Termios) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
}

func makeCooked(t *unix.Termios) {
	t.Iflag |= unix.ICRNL | unix.IXON | unix.BRKINT
	t.Oflag |= unix.OPOST | unix.ONLCR
	t.Lflag |= unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ICANON | unix.ISIG | unix.IEXTEN
}

var queryText = map[string]string{
	"osc10": "\x1b]10;?\x1b\\",
	"osc11": "\x1b]11;?\x1b\\",
	"osc12": "\x1b]12;?\x1b\\",
	"da1":   "\x1b[c",
}

func (a *agent) query(args []string) error {
	var q []byte
	da1 := false
	for _, n := range strings.Split(args[0], ",") {
		s, ok := queryText[n]
		if !ok {
			return fmt.Errorf("unknown query %q", n)
		}
		da1 = da1 || n == "da1"
		q = append(q, s...)
	}
	old, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return err
	}
	raw := *old
	makeRaw(&raw)
	if err := unix.IoctlSetTermios(0, unix.TCSETS, &raw); err != nil {
		return err
	}
	defer unix.IoctlSetTermios(0, unix.TCSETS, old)
	if err := a.out(q); err != nil {
		return err
	}
	var got []byte
	buf := make([]byte, 1024)
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < maxQueryRead && time.Now().Before(deadline) {
		if da1 && hasDA1Reply(got) {
			break
		}
		fds := []unix.PollFd{{Fd: 0, Events: unix.POLLIN}}
		wait := 1000
		if da1 {
			wait = 100
		}
		n, err := unix.Poll(fds, wait)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			if da1 {
				continue // wait for the sentinel until the deadline
			}
			break // quiet
		}
		m, err := unix.Read(0, buf)
		if err != nil || m == 0 {
			break
		}
		got = append(got, buf[:m]...)
	}
	return writeFileAtomic(args[1], got)
}

// hasDA1Reply reports whether b holds a primary device attributes reply,
// CSI ? ... c.
func hasDA1Reply(b []byte) bool {
	i := bytes.Index(b, []byte("\x1b[?"))
	return i >= 0 && bytes.IndexByte(b[i:], 'c') >= 0
}

// floodGen is the deterministic byte source of flood: lines of 62 printable
// bytes and CR LF, the printable bytes drawn from the seed one at a time, so
// the stream does not depend on how it is cut into writes.
type floodGen struct {
	rng *rand.Rand
	pos int
}

func newFloodGen(seed int64) *floodGen { return &floodGen{rng: rand.New(rand.NewSource(seed))} }

func (g *floodGen) fill(b []byte) {
	for i := range b {
		switch g.pos % 64 {
		case 62:
			b[i] = '\r'
		case 63:
			b[i] = '\n'
		default:
			b[i] = byte(0x20 + g.rng.Intn(95))
		}
		g.pos++
	}
}

// FloodBytes returns the first n bytes the flood step writes for a seed.
func FloodBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	newFloodGen(seed).fill(b)
	return b
}

func (a *agent) flood(args []string) error {
	rate, err1 := strconv.ParseInt(args[0], 10, 64)
	total, err2 := strconv.ParseInt(args[1], 10, 64)
	seed, err3 := strconv.ParseInt(args[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || rate < 0 || total < 0 {
		return errors.New("flood wants RATE TOTAL SEED as numbers (RATE and TOTAL not negative)")
	}
	g := newFloodGen(seed)
	buf := make([]byte, floodChunk)
	const step = 10 * time.Millisecond
	perTick := rate * int64(step) / int64(time.Second)
	if rate > 0 && perTick < 1 {
		perTick = 1
	}
	var tick *time.Ticker
	if rate > 0 {
		tick = time.NewTicker(step)
		defer tick.Stop()
	}
	for sent := int64(0); total == 0 || sent < total; {
		if tick != nil {
			<-tick.C
		}
		want := int64(len(buf))
		if tick != nil {
			want = perTick
		}
		if total > 0 && total-sent < want {
			want = total - sent
		}
		for want > 0 {
			n := min(want, int64(len(buf)))
			g.fill(buf[:n])
			if err := a.out(buf[:n]); err != nil {
				return err
			}
			sent += n
			want -= n
		}
	}
	return nil
}

func (a *agent) spawn(args []string) error {
	kind, file := args[0], args[1]
	detach := false
	ttl := time.Minute
	for _, o := range args[2:] {
		switch {
		case o == "detach":
			detach = true
		case strings.HasPrefix(o, "ttl="):
			d, err := time.ParseDuration(strings.TrimPrefix(o, "ttl="))
			if err != nil {
				return err
			}
			ttl = d
		default:
			return fmt.Errorf("unknown spawn option %q", o)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, EnvScript+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	// It ignores SIGHUP: when the agent, the session leader, exits, the
	// kernel hangs up the foreground group, which a lingering child of a real
	// agent would also have to survive.
	cmd.Env = append(cmd.Env, EnvScript+"=ignore HUP; pidfile "+file+"; sleep "+ttl.String())
	if !detach {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	switch kind {
	case "same":
	case "group":
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	case "session":
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	default:
		return fmt.Errorf("unknown spawn kind %q", kind)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	// Return only when the copy is ready (it has ignored SIGHUP and written
	// its pid), so that a step after this one cannot race its start-up.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file); err == nil {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return errors.New("the spawned copy did not start")
}

func (a *agent) pidfile(args []string) error {
	return writeFileAtomic(args[0], []byte(strconv.Itoa(os.Getpid())+"\n"))
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// writeFileAtomic writes path whole: a reader sees no file, or all of it.
func writeFileAtomic(path string, b []byte) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
