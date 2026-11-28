//go:build linux

package serve

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The counter pump belongs to the canary command. Run its fake-host fixture
// with probes at the production receive sites, including the pump's own tick.
func TestMeasureHolderWakes(t *testing.T) {
	if os.Getenv("ATANIQ_MEASURE_WAKES") != "1" {
		t.Skip("set ATANIQ_MEASURE_WAKES=1 on the coordinator")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	overlay := wakeOverlay(t, root)
	binary := filepath.Join(t.TempDir(), "holder.test")
	build := exec.CommandContext(t.Context(), "sh", "scripts/go-tool.sh", "test", "-c", "-o", binary,
		"-overlay", overlay, "-tags", "ataniq_canary", "./cmd/ataniq-holder/")
	build.Dir, build.Stdout, build.Stderr = root, os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	var timeout time.Duration
	if deadline, ok := t.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.v", "-test.timeout="+timeout.String(), "-test.run=^TestWakesWithCanaryHolder$")
	cmd.Dir, cmd.Stdout, cmd.Stderr = filepath.Join(root, "cmd/ataniq-holder"), os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

// Only the instrumented build sees these files. Require a unique insertion
// point so that a change to a loop cannot silently give a zero count.
func wakeOverlay(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	replacements := make(map[string]string)
	write := func(name, contents string) {
		path := filepath.Join(dir, filepath.Base(name))
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		replacements[filepath.Join(root, name)] = path
	}
	type insertion struct{ after, statement string }
	probe := func(name string, edits ...insertion) {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, edit := range edits {
			if strings.Count(src, edit.after) != 1 {
				t.Fatalf("wake probe in %s needs one occurrence of %q", name, edit.after)
			}
			src = strings.Replace(src, edit.after, edit.after+"\n"+edit.statement, 1)
		}
		write(name, src)
	}

	const serveDir = "internal/leadholder/serve/"
	// Include the observer below in the command's copy of package serve.
	replacements[filepath.Join(root, serveDir+"measure_observer_linux.go")] =
		filepath.Join(root, serveDir+"measure_wakes_linux_test.go")
	probe(serveDir+"finish_linux.go",
		insertion{"case <-s.kick:", "RecordHolderWake(s.sess, wakeKick)"},
		insertion{"\n\t\tcase <-timer.C:", "RecordHolderWake(s.sess, wakeTimer)"},
		insertion{"s.nextFacts = now.Add(s.cfg.PollInterval)", "RecordHolderWake(s.sess, factsPoll)"})
	probe(serveDir+"sender_linux.go",
		insertion{"case <-c.wake:", "RecordHolderWake(s.sess, wakeSender)"})
	probe("cmd/ataniq-holder/canary_linux.go",
		insertion{"\n\t\tcase <-t.C:", "serve.RecordHolderWake(p.sess, serve.WakeCounterPump)"})
	write("cmd/ataniq-holder/measure_fixture_linux_test.go", wakeFixture)
	b, err := json.Marshal(map[string]any{"Replace": replacements})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The adapter lives in package main only in the overlay. Keep the real start
// path, session, pump and sockets. The rig supplies the fake host and launcher.
const wakeFixture = `package main

import (
	"testing"

	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
)

func TestWakesWithCanaryHolder(t *testing.T) {
	serve.MeasureWakeCells(t, func(t *testing.T) serve.WakeHolder {
		c := newCanaryRig(t)
		c.interval = 0 // production counterInterval
		c.script = "raw; say READY; wait"
		args := c.spec(c.digest()).build()
		d := c.deps()
		d.tweakServe = nil // production PollInterval
		started := make(chan *session.Session, 1)
		d.canary.pumpStarted = func(p *counterPump) { started <- p.sess }
		go func() { c.done <- run(args, d) }()
		c.waitReady()
		t.Cleanup(c.stopAndWait)
		var s *session.Session
		select {
		case s = <-started:
		default:
			t.Fatal("holder became ready without starting the counter pump")
		}
		poll(t, func() string { return "the fake agent's READY line" }, func() bool {
			return s.Counters()["output_bytes"] == uint64(len("READY\r\n"))
		})
		return serve.WakeHolder{Session: s, Dir: c.runtime, Ticket: c.newTicket, Status: c.status}
	})
}
`

// WakeHolder is the part of the running command fixture used by the sampler.
type WakeHolder struct {
	Session *session.Session
	Dir     string
	Ticket  func(wire.Role) string
	Status  func() wire.Status
}

func (h WakeHolder) attach(t *testing.T) *leadattach.Client {
	t.Helper()
	path := filepath.Join(h.Dir, "ticket")
	if err := os.WriteFile(path, []byte(h.Ticket(wire.RoleWriter)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A descriptor-relative socket name also fits for long temporary paths.
	dir, err := os.Open(h.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	tickets, ready := leadattach.FileTicketSource(path)
	client := leadattach.New(leadattach.Config{
		Socket: fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), AttachSocket),
		Mode:   leadattach.ModeWriter, Tickets: tickets, TicketReady: ready,
	})
	pane := sessionpane.New(100, 30, sessionpane.Options{})
	t.Cleanup(pane.Close)
	t.Cleanup(client.Close)
	if _, err := client.Start(terminalconsole.LiveStart{Pane: pane, Cols: 100, Rows: 30, Colours: sessionpane.NeutralColours}); err != nil {
		t.Fatal(err)
	}
	// The client publishes LiveAttached only after restoring its snapshot.
	deadline := time.Now().Add(2 * DefaultHandshakeTimeout)
	for client.Status().Conn != terminalconsole.LiveAttached {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for the initial snapshot: %+v", client.Status())
		}
		time.Sleep(DefaultPollInterval)
	}
	return client
}

func (h WakeHolder) check(t *testing.T, client *leadattach.Client) {
	t.Helper()
	st := h.Status()
	if st.WriterPresent != (client != nil) || st.ViewerPresent || st.Facts.TUIExited != wire.FactNo ||
		st.Counters["output_bytes"] != uint64(len("READY\r\n")) {
		t.Fatalf("holder left the measured state: %+v", st)
	}
	if client != nil {
		status := client.Status()
		if status.Conn != terminalconsole.LiveAttached || status.CheckMismatches != 0 || status.Resyncs != 0 {
			t.Fatalf("attach did not stay in sync: %+v", status)
		}
	}
}

const (
	wakeTimer = iota
	wakeKick
	wakeSender
	WakeCounterPump
	factsPoll // work inside a timekeeper wake, excluded from the sum
)

var wakeNames = [...]string{"timekeeper_timer", "timekeeper_kick", "sender", "counter_pump", "facts_polls"}

type wakeCounts [len(wakeNames)]int64

var wakeSample struct {
	sync.Mutex
	from, until time.Time
	holders     map[*session.Session]*wakeCounts
}

// RecordHolderWake observes one selected branch. All holders share the same
// monotonic half-open interval, even if the measuring goroutine wakes late.
func RecordHolderWake(s *session.Session, source int) {
	wakeSample.Lock()
	defer wakeSample.Unlock()
	if counts := wakeSample.holders[s]; counts != nil {
		now := time.Now()
		if !now.Before(wakeSample.from) && now.Before(wakeSample.until) {
			counts[source]++
		}
	}
}

type wakeRate struct {
	Counts    map[string]int64 `json:"counts"`
	Wakes     int64            `json:"wakes"`
	PerSecond float64          `json:"wakes_per_second"`
}

func (counts wakeCounts) rate(interval time.Duration) wakeRate {
	r := wakeRate{Counts: make(map[string]int64)}
	for i, name := range wakeNames {
		r.Counts[name] = counts[i]
		if i != factsPoll {
			r.Wakes += counts[i]
		}
	}
	r.PerSecond = float64(r.Wakes) / interval.Seconds()
	return r
}

type wakeCell struct {
	Mode      string     `json:"mode"`
	Holders   int        `json:"holders"`
	Seconds   float64    `json:"seconds"`
	PerHolder []wakeRate `json:"per_holder"`
	Aggregate wakeRate   `json:"aggregate"`
}

// MeasureWakeCells is the fixture entry point in the instrumented build.
func MeasureWakeCells(t *testing.T, start func(*testing.T) WakeHolder) {
	interval := time.Minute
	if raw, ok := os.LookupEnv("ATANIQ_MEASURE_INTERVAL"); ok {
		var err error
		interval, err = time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			t.Fatalf("ATANIQ_MEASURE_INTERVAL must be a positive Go duration, got %q", raw)
		}
	}
	var cells []wakeCell
	for _, n := range []int{1, 8} {
		for _, mode := range []string{"quiet", "attached"} {
			cell := wakeCell{Mode: mode, Holders: n, Seconds: interval.Seconds()}
			if !t.Run(fmt.Sprintf("%d_%s", n, mode), func(t *testing.T) {
				counts := make([]wakeCounts, n)
				holders := make(map[*session.Session]*wakeCounts, n)
				var checks []func()
				for i := range counts {
					h := start(t)
					var client *leadattach.Client
					if mode == "attached" {
						client = h.attach(t)
					}
					holders[h.Session] = &counts[i]
					checks = append(checks, func() { h.check(t, client) })
				}
				for _, check := range checks {
					check()
				}
				wakeSample.Lock()
				from := time.Now().Add(2 * DefaultPollInterval)
				until := from.Add(interval)
				wakeSample.from, wakeSample.until, wakeSample.holders = from, until, holders
				wakeSample.Unlock()
				time.Sleep(time.Until(until))
				wakeSample.Lock()
				wakeSample.holders = nil
				wakeSample.Unlock()
				for _, check := range checks {
					check()
				}
				var total wakeCounts
				for _, c := range counts {
					cell.PerHolder = append(cell.PerHolder, c.rate(interval))
					for source, count := range c {
						total[source] += count
					}
				}
				cell.Aggregate = total.rate(interval)
			}) {
				return
			}
			cells = append(cells, cell)
		}
	}
	fmt.Printf("Canary holder wakeups over %s per cell\n", interval)
	fmt.Println("holders mode     holder    timer   kick sender   pump facts_polls    wakes/s")
	for _, cell := range cells {
		printRate := func(who string, r wakeRate) {
			fmt.Printf("%7d %-8s %6s %8d %6d %6d %6d %11d %10.2f\n", cell.Holders, cell.Mode, who,
				r.Counts["timekeeper_timer"], r.Counts["timekeeper_kick"], r.Counts["sender"],
				r.Counts["counter_pump"], r.Counts["facts_polls"], r.PerSecond)
		}
		for i, r := range cell.PerHolder {
			printRate(fmt.Sprint(i+1), r)
		}
		printRate("all", cell.Aggregate)
	}
	if err := json.NewEncoder(os.Stdout).Encode(cells); err != nil {
		t.Fatal(err)
	}
}
