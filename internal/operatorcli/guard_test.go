package operatorcli

import (
	"context"
	"strings"
	"testing"

	"ataniq/internal/appmodel"
)

type replyFake struct {
	calls   int
	receipt appmodel.AdmissionReceipt
	status  appmodel.CurrentStatus
}

func (f *replyFake) Submit(context.Context, appmodel.Envelope) (appmodel.AdmissionReceipt, error) {
	f.calls++
	return f.receipt, nil
}
func (f *replyFake) Show(context.Context, appmodel.CommandKey) (appmodel.CurrentStatus, error) {
	f.calls++
	return f.status, nil
}

const unknownLine = "{\"kind\":\"observation_error\",\"code\":\"unknown\"}\n"

func TestMalformedBackendRepliesAreUnknown(t *testing.T) {
	good := appmodel.AdmissionReceipt{Command: appmodel.CommandKey{Authority: "a", EnrolledClientID: "trusted", CommandID: "cmd-1"}, Digest: [32]byte{1}, AdmittedRevision: "r2", Cursor: "c1"}
	for name, mutate := range map[string]func(*appmodel.AdmissionReceipt){
		"other command id": func(r *appmodel.AdmissionReceipt) { r.Command.CommandID = "cmd-2" },
		"invalid key":      func(r *appmodel.AdmissionReceipt) { r.Command.EnrolledClientID = "" },
		"invalid revision": func(r *appmodel.AdmissionReceipt) { r.AdmittedRevision = "r 2" },
		"long revision": func(r *appmodel.AdmissionReceipt) {
			r.AdmittedRevision = appmodel.Revision(strings.Repeat("r", appmodel.MaxIDBytes+1))
		},
		"empty cursor": func(r *appmodel.AdmissionReceipt) { r.Cursor = "" },
		"zero digest":  func(r *appmodel.AdmissionReceipt) { r.Digest = [32]byte{} },
	} {
		r := good
		mutate(&r)
		f := &replyFake{receipt: r}
		if exit, out := run2(f, submit, command); exit != 1 || f.calls != 1 || out != unknownLine {
			t.Fatal(name, exit, out)
		}
	}
	f := &replyFake{receipt: good}
	if exit, out := run2(f, submit, command); exit != 0 || out != "{\"kind\":\"receipt\",\"command\":{\"authority\":\"a\",\"client_id\":\"trusted\",\"id\":\"cmd-1\"},\"digest\":\"01"+strings.Repeat("0", 62)+"\",\"admitted_revision\":\"r2\",\"cursor\":\"c1\"}\n" {
		t.Fatal(exit, out)
	}

	k := appmodel.CommandKey{Authority: "a", EnrolledClientID: "other", CommandID: "cmd-1"}
	for name, s := range map[string]appmodel.CurrentStatus{
		"other client":   {Command: appmodel.CommandKey{Authority: "a", EnrolledClientID: "x", CommandID: "cmd-1"}, Revision: "r3", State: appmodel.Held},
		"empty revision": {Command: k, State: appmodel.Held},
		"unknown state":  {Command: k, Revision: "r3", State: "running"},
	} {
		f := &replyFake{status: s}
		if exit, out := run2(f, show, ""); exit != 1 || f.calls != 1 || out != unknownLine {
			t.Fatal(name, exit, out)
		}
	}
	f = &replyFake{status: appmodel.CurrentStatus{Command: k, Revision: "r3", State: appmodel.Applied}}
	if exit, out := run2(f, show, ""); exit != 0 || out != "{\"kind\":\"status\",\"command\":{\"authority\":\"a\",\"client_id\":\"other\",\"id\":\"cmd-1\"},\"revision\":\"r3\",\"state\":\"applied\"}\n" {
		t.Fatal(exit, out)
	}
}

func run2(b Backend, args []string, input string) (int, string) {
	var out strings.Builder
	code := Run(context.Background(), b, args, strings.NewReader(input), &out)
	return code, out.String()
}

func TestShowPreCancelledIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := new(fake)
	var out strings.Builder
	if code := Run(ctx, f, show, nil, &out); code != 1 || f.calls != 0 || out.String() != "{\"kind\":\"observation_error\",\"code\":\"unavailable\"}\n" {
		t.Fatal(code, out.String())
	}
}

func TestUsageErrorsNeverCallBackend(t *testing.T) {
	for _, args := range [][]string{
		{"command", "submit", "--file", "-"},
		{"command", "submit", "--file", "-", "--format", "text"},
		{"command", "submit", "--file", "-", "--format", "json", "--file", "-"},
		{"command", "submit", "--file", "-", "--format", "json", "--extra", "x"},
		{"command", "show", "--authority", "a", "--client", "other", "--id", "cmd-1", "--format", "json", "--extra", "x"},
		{"command", "show", "--authority", "a", "--client", "other", "--id", strings.Repeat("i", appmodel.MaxIDBytes+1), "--format", "json"},
	} {
		f := new(fake)
		if exit, out := run(f, args, command); exit != 2 || f.calls != 0 || out != "{\"kind\":\"domain_error\",\"code\":\"invalid_input\"}\n" {
			t.Fatal(args, exit, out)
		}
	}
	f := new(fake)
	if exit, out := run(f, []string{"command", "steer", "--format", "json"}, ""); exit != 2 || f.calls != 0 || out != "{\"kind\":\"domain_error\",\"code\":\"unsupported_capability\"}\n" {
		t.Fatal(exit, out)
	}
	if exit, out := run(f, submit, strings.Replace(command, `"schema_version":1`, `"schema_version":2`, 1)); exit != 2 || f.calls != 0 || out != "{\"kind\":\"domain_error\",\"code\":\"unsupported_version\"}\n" {
		t.Fatal(exit, out)
	}
}

func TestSubmitCancelOperation(t *testing.T) {
	cancelCommand := `{"schema_version":1,"command_id":"cmd-2","project":{"authority":"a","id":"p"},"target":{"authority":"a","kind":"command","client_id":"trusted","id":"cmd-1"},"operation":"queue.cancel","expected_revision":"r1","payload":{}}`
	f := new(fake)
	if exit, out := run(f, submit, cancelCommand); exit != 0 || f.calls != 1 || !strings.Contains(out, `"id":"cmd-2"`) {
		t.Fatal(exit, out)
	}
	if op, ok := f.envelope.Operation.(appmodel.Cancel); !ok || op.Target != (appmodel.CommandKey{Authority: "a", EnrolledClientID: "trusted", CommandID: "cmd-1"}) {
		t.Fatal(f.envelope.Operation)
	}
}

func TestHiddenOrAbsentStaysDomainCode(t *testing.T) {
	for _, args := range [][]string{submit, show} {
		f := &fake{err: appmodel.DomainError{Code: appmodel.HiddenOrAbsent}}
		if exit, out := run(f, args, command); exit != 1 || out != "{\"kind\":\"domain_error\",\"code\":\"hidden_or_absent\"}\n" {
			t.Fatal(exit, out)
		}
	}
}
