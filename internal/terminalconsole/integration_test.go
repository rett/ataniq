package terminalconsole

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// syncBuffer is an output a program writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// probe wraps the console to tell the test, from the program's own goroutine,
// when something happened.
type probe struct {
	*Model
	reported, ended chan struct{}
	once            [2]sync.Once
}

func (p *probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.Model.Update(msg)
	if _, ok := msg.(tea.ModeReportMsg); ok {
		p.once[0].Do(func() { close(p.reported) })
	}
	if p.Model.sessionEnded() {
		p.once[1].Do(func() { close(p.ended) })
	}
	return p, cmd
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCockpitPaneAndReplayInAProgram runs the console in a real Bubble Tea
// program over a pipe, with SSH_TTY set: the replay is driven by tea.Tick, the
// pane emulates its output, and the answers of the outer terminal come back
// through the input. It runs under the race detector.
func TestCockpitPaneAndReplayInAProgram(t *testing.T) {
	for _, recording := range []string{"exit-zero.jsonl", "queries.jsonl", "sync-frames.jsonl", "title.jsonl"} {
		t.Run(recording, func(t *testing.T) {
			p := &probe{
				Model:    NewWithOptions(Fixture{}, Options{Theme: "dark", Recording: recording}),
				reported: make(chan struct{}), ended: make(chan struct{}),
			}
			defer p.Close()
			in, inw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			defer inw.Close()
			out := &syncBuffer{}
			prog := tea.NewProgram(p, tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(140, 45), tea.WithoutSignalHandler(),
				tea.WithEnvironment([]string{"SSH_TTY=/dev/pts/7", "TERM=xterm-256color"}))
			done := make(chan error, 1)
			go func() {
				_, err := prog.Run()
				done <- err
			}()
			// The console asks for mode 2027 whatever SSH_TTY says, and does not
			// ask for 2026, which would turn synchronized output on over SSH.
			waitFor(t, "the mode 2027 query", func() bool { return strings.Contains(out.String(), ansi.RequestModeUnicodeCore) })
			if strings.Contains(out.String(), ansi.RequestModeSynchronizedOutput) {
				t.Fatal("the console queried mode 2026 over SSH")
			}
			if _, err := inw.WriteString("\x1b[?2027;2$y"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-p.reported:
			case <-time.After(10 * time.Second):
				t.Fatal("the console never saw the mode report")
			}
			select {
			case <-p.ended:
			case <-time.After(10 * time.Second):
				t.Fatal("the session never ended")
			}
			if _, err := inw.WriteString("q"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the program did not quit on q")
			}
			if !p.graphemes {
				t.Fatal("the mode report did not reach the console")
			}
			if v := ansi.Strip(p.View().Content); !strings.Contains(v, "TERMINAL STREAM ENDED") || !strings.Contains(v, fixtureLabel) {
				t.Fatal(v)
			}
			// Nothing the session wrote came out as a sequence: the program
			// drew cells, and asked the terminal only for what it needs.
			o := out.String()
			for _, bad := range []string{"\x1b]0;", "\x1b]2;", "\x1b]52;", "\x1b]1337", "\x1bP", "\x1b_"} {
				if strings.Contains(o, bad) {
					t.Fatalf("the output has %q", bad)
				}
			}
			if !strings.Contains(o, "\x1b[?1049h") {
				t.Fatal("not on the alternate screen")
			}
		})
	}
}
