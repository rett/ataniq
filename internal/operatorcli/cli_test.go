package operatorcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/appmodel"
)

const command = `{"schema_version":1,"command_id":"cmd-1","project":{"authority":"a","id":"p"},"target":{"authority":"a","kind":"conversation","id":"c"},"operation":"input.enqueue","expected_revision":"r1","payload":{"text":"private"}}`

type fake struct {
	calls    int
	envelope appmodel.Envelope
	key      appmodel.CommandKey
	err      error
}

func (f *fake) Submit(_ context.Context, e appmodel.Envelope) (appmodel.AdmissionReceipt, error) {
	f.calls++
	f.envelope = e
	return appmodel.AdmissionReceipt{Command: appmodel.CommandKey{Authority: "a", EnrolledClientID: "trusted", CommandID: e.CommandID}, AdmittedRevision: "r2", Cursor: "c1"}, f.err
}
func (f *fake) Show(_ context.Context, k appmodel.CommandKey) (appmodel.CurrentStatus, error) {
	f.calls++
	f.key = k
	return appmodel.CurrentStatus{Command: k, Revision: "r3", State: appmodel.Held}, f.err
}
func run(f *fake, args []string, input string) (int, string) {
	var out bytes.Buffer
	code := Run(context.Background(), f, args, strings.NewReader(input), &out)
	return code, out.String()
}

var submit = []string{"command", "submit", "--file", "-", "--format", "json"}
var show = []string{"command", "show", "--authority", "a", "--client", "other", "--id", "cmd-1", "--format", "json"}

func TestSubmitAndShow(t *testing.T) {
	f := new(fake)
	code, out := run(f, submit, command)
	if code != 0 || !strings.Contains(out, `"admitted_revision":"r2"`) || strings.Contains(out, "private") {
		t.Fatal(code, out)
	}
	want, err := appmodel.Decode([]byte(command))
	if err != nil || f.envelope.CommandID != want.CommandID || f.envelope.Operation != want.Operation {
		t.Fatal(f.envelope, err)
	}
	code, out = run(f, show, "")
	if code != 0 || f.key != (appmodel.CommandKey{Authority: "a", EnrolledClientID: "other", CommandID: "cmd-1"}) || !strings.Contains(out, `"state":"held"`) {
		t.Fatal(code, out, f.key)
	}
}
func TestInvalidNeverCallsBackend(t *testing.T) {
	inputs := []string{"{", strings.Repeat("x", appmodel.MaxWireBytes+1), string([]byte{0xff}), strings.Replace(command, `"schema_version":1`, `"schema_version":2`, 1), strings.Replace(command, `"command_id"`, `"bad"`, 1)}
	for _, input := range inputs {
		f := new(fake)
		code, out := run(f, submit, input)
		if code != 2 || f.calls != 0 || !strings.HasSuffix(out, "\n") || strings.Contains(out, "private") {
			t.Fatal(code, out, f.calls)
		}
	}
	f := new(fake)
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(command), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"command", "submit", "--file", path, "--format", "json"}
	if code, _ := run(f, args, ""); code != 0 || f.calls != 1 {
		t.Fatal(code, f.calls)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", appmodel.MaxWireBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(f, args, ""); code != 2 || f.calls != 1 {
		t.Fatal(code, f.calls)
	}
	for _, args := range [][]string{{"help"}, {"completion", "bash"}, {"completion", "zsh"}, {"completion", "fish"}, {"command", "show", "--client", "x"}} {
		f := new(fake)
		_, out := run(f, args, "")
		if f.calls != 0 || out == "" {
			t.Fatal(args, out)
		}
	}
}
func TestErrorsAndCancellation(t *testing.T) {
	for _, code := range []appmodel.Code{appmodel.UnsupportedCapability, appmodel.PermissionDenied, appmodel.BindingConflict, appmodel.ResetRequired, appmodel.CapacityExceeded} {
		f := &fake{err: appmodel.DomainError{Code: code}}
		exit, out := run(f, submit, command)
		if exit != 1 || !strings.Contains(out, string(code)) || f.calls != 1 {
			t.Fatal(exit, out)
		}
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{ErrUnavailable, "unavailable"}, {errors.New("secret"), "unknown"}, {appmodel.DomainError{Code: "secret"}, "unknown"}} {
		f := &fake{err: tc.err}
		exit, out := run(f, submit, command)
		if exit != 1 || !strings.Contains(out, tc.code) || strings.Contains(out, "secret") {
			t.Fatal(exit, out)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := new(fake)
	var out bytes.Buffer
	if code := Run(ctx, f, submit, strings.NewReader(command), &out); code != 1 || f.calls != 0 || !strings.Contains(out.String(), "unavailable") {
		t.Fatal(code, out.String())
	}
	f = new(fake)
	if code := Run(context.Background(), f, submit, strings.NewReader(command), badWriter{}); code != 3 || f.calls != 1 {
		t.Fatal(code, f.calls)
	}
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("failed") }
