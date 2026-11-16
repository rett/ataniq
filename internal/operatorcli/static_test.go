package operatorcli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

const invalidLine = "{\"kind\":\"domain_error\",\"code\":\"invalid_input\"}\n"
const unavailableLine = "{\"kind\":\"observation_error\",\"code\":\"unavailable\"}\n"

func TestStaticTextAndLocalOutcomes(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}} {
		if exit, out := run(new(fake), args, ""); exit != 0 || out != help || !strings.HasPrefix(out, "Usage: ataniq command submit ") {
			t.Fatal(args, exit, out)
		}
	}
	for shell, want := range map[string]string{
		"bash": "complete -W 'command help completion' ataniq\n",
		"zsh":  "#compdef ataniq\n_arguments '1:command:(command help completion)'\n",
		"fish": "complete -c ataniq -f -a 'command help completion'\n",
	} {
		if exit, out := run(new(fake), []string{"completion", shell}, ""); exit != 0 || out != want {
			t.Fatal(shell, exit, out)
		}
	}
	for _, args := range [][]string{
		{"bogus"},
		{"help", "x"},
		{"completion", "powershell"},
		{"command", "submit", "--file", "/nonexistent/input.json", "--format", "json"},
		{"command", "submit", "--file", "-", "--format", "", "--format", "json"},
	} {
		f := new(fake)
		if exit, out := run(f, args, command); exit != 2 || f.calls != 0 || out != invalidLine {
			t.Fatal(args, exit, out)
		}
	}
	var out strings.Builder
	if exit := Run(context.Background(), new(fake), submit, nil, &out); exit != 2 || out.String() != invalidLine {
		t.Fatal(exit, out.String())
	}
	for _, args := range [][]string{submit, show} {
		out.Reset()
		if exit := Run(context.Background(), nil, args, strings.NewReader(command), &out); exit != 1 || out.String() != unavailableLine {
			t.Fatal(args, exit, out.String())
		}
		f := &fake{err: fmt.Errorf("wrapped: %w", ErrUnavailable)}
		if exit, got := run(f, args, command); exit != 1 || f.calls != 1 || got != unavailableLine {
			t.Fatal(args, exit, got)
		}
	}
}

type cancellingReader struct {
	r      io.Reader
	cancel func()
}

func (c cancellingReader) Read(p []byte) (int, error) { c.cancel(); return c.r.Read(p) }

func TestCancelledWhileReadingIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := new(fake)
	var out strings.Builder
	if exit := Run(ctx, f, submit, cancellingReader{strings.NewReader(command), cancel}, &out); exit != 1 || f.calls != 0 || out.String() != unavailableLine {
		t.Fatal(exit, f.calls, out.String())
	}
}
