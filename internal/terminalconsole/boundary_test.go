package terminalconsole

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type sample struct{ p Projection }

func (s sample) Snapshot() Projection { return s.p }

func TestSelectionAndNoticeBoundaries(t *testing.T) {
	p := Projection{ControlUnavailable: "fixture controls disabled"}
	for i := 0; i < 8; i++ {
		var leads []Lead
		for j := 0; j < 3; j++ {
			leads = append(leads, Lead{Name: fmt.Sprintf("lead-%d", j), Subagents: []string{"one", "two"}, Conversation: []string{"hello"}})
		}
		p.Projects = append(p.Projects, Project{Name: fmt.Sprintf("project-%d", i), Leads: leads})
	}
	for _, h := range []int{2, 3, 8} {
		m := New(sample{p}, "dark")
		size(m, 80, h)
		for i := 0; i < 8; i++ {
			if !strings.Contains(content(m), fmt.Sprintf("> project-%d", i)) {
				t.Fatalf("height %d: %s", h, content(m))
			}
			key(m, 'j')
		}
		enter(m)
		for i := 0; i < 3; i++ {
			if !strings.Contains(content(m), fmt.Sprintf("> lead-%d", i)) {
				t.Fatalf("height %d: %s", h, content(m))
			}
			key(m, 'j')
		}
		if cmd := key(m, 'a'); cmd != nil {
			t.Fatal("control returned command")
		}
		if h >= 3 && !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
		if !strings.Contains(content(m), "> lead-2") {
			t.Fatal(content(m))
		}
		size(m, 80, 1)
		if !strings.Contains(content(m), "DEMO") {
			t.Fatal(content(m))
		}
		size(m, 80, 3)
		if !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
		enter(m)
		key(m, 'j')
		if !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
	}
}

func TestCleanBoundedUTF8(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 511) + "😀", strings.Repeat("\x1b", 5000) + "visible", "a\xff\x1b]0;x\a\u009b\u202e\u2028z"} {
		got := clean(input)
		if !utf8.ValidString(got) || len(got) > 512 || strings.ContainsAny(got, "\x1b\a\u009b\u202e\u2028") {
			t.Fatalf("unsafe: %q", got)
		}
	}
	if got := clean(strings.Repeat("\x1b", 5000) + "visible"); got != "" {
		t.Fatal(got)
	}
	if got := clean(strings.Repeat("x", 511) + "😀"); len(got) != 511 {
		t.Fatal(len(got))
	}
}

func TestExtremeResizeAfterScroll(t *testing.T) {
	m := New(Fixture{}, "dark")
	t.Cleanup(m.Close)
	for _, page := range []string{"projects", "leads", "tasks", "conversation", "help", "actions"} {
		// The pages show below the cockpit's size.
		m.page, m.scroll = page, 3
		size(m, 99, math.MaxInt)
		if len(m.View().Content) > 100000 {
			t.Fatal("unbounded view")
		}
		size(m, 99, 99)
		size(m, math.MaxInt, math.MaxInt)
		size(m, 99, math.MaxInt)
		if len(m.View().Content) > 100000 {
			t.Fatal("unbounded view")
		}
	}
	// The cockpit draws at most its canvas, whatever the window.
	size(m, math.MaxInt, math.MaxInt)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) > maxCanvasRows {
		t.Fatalf("%d rows", len(lines))
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > maxCanvasCols {
			t.Fatalf("%d columns", ansi.StringWidth(l))
		}
	}
}
