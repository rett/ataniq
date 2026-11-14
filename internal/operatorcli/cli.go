// Package operatorcli provides a provisional, transport-independent CLI adapter.
package operatorcli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"ataniq/internal/appmodel"
)

// Backend is composed with trusted authentication and session context by its caller.
// Cancellation of ctx detaches observation; it must not cancel durable work.
// Domain errors are authoritative result codes, not necessarily proof of admission
// or nonadmission. StorageUnknown remains uncertain. The backend must enforce
// current authentication, permissions, target scope and disclosure on each call.
type Backend interface {
	Submit(context.Context, appmodel.Envelope) (appmodel.AdmissionReceipt, error)
	Show(context.Context, appmodel.CommandKey) (appmodel.CurrentStatus, error)
}

// ErrUnavailable may only be returned when the backend can prove it did not invoke the service.
var ErrUnavailable = errors.New("service unavailable before invocation")

const help = "Usage: ataniq command submit --file PATH --format json\n       ataniq command show --authority A --client C --id ID --format json\n       ataniq completion bash|zsh|fish\n       ataniq help\n"

const maxCursorBytes = 512

var completions = map[string]string{
	"bash": "complete -W 'command help completion' ataniq\n",
	"zsh":  "#compdef ataniq\n_arguments '1:command:(command help completion)'\n",
	"fish": "complete -c ataniq -f -a 'command help completion'\n",
}

type key struct {
	Authority string             `json:"authority"`
	Client    string             `json:"client_id"`
	ID        appmodel.CommandID `json:"id"`
}
type result struct {
	Kind             string            `json:"kind"`
	Code             string            `json:"code,omitempty"`
	Command          *key              `json:"command,omitempty"`
	Digest           string            `json:"digest,omitempty"`
	AdmittedRevision appmodel.Revision `json:"admitted_revision,omitempty"`
	Cursor           string            `json:"cursor,omitempty"`
	Revision         appmodel.Revision `json:"revision,omitempty"`
	State            appmodel.State    `json:"state,omitempty"`
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > appmodel.MaxIDBytes {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", rune(c))) {
			return false
		}
	}
	return true
}
func validKey(k appmodel.CommandKey) bool {
	return validID(k.Authority) && validID(k.EnrolledClientID) && validID(string(k.CommandID))
}
func wireKey(k appmodel.CommandKey) *key { return &key{k.Authority, k.EnrolledClientID, k.CommandID} }
func emit(w io.Writer, r result, code int) int {
	b, err := json.Marshal(r)
	if err != nil || len(b) > 4096 {
		return failure(w, "observation_error", "unknown", 1)
	}
	b = append(b, '\n')
	n, err := w.Write(b)
	if err != nil || n != len(b) {
		return 3
	}
	return code
}
func failure(w io.Writer, kind, code string, exit int) int {
	return emit(w, result{Kind: kind, Code: code}, exit)
}
func domain(err error) (string, bool) {
	var e appmodel.DomainError
	if !errors.As(err, &e) {
		var p *appmodel.DomainError
		if !errors.As(err, &p) || p == nil {
			return "", false
		}
		e = *p
	}
	switch e.Code {
	case appmodel.InvalidInput, appmodel.UnsupportedVersion, appmodel.UnsupportedCapability, appmodel.Unauthenticated, appmodel.PermissionDenied, appmodel.HiddenOrAbsent, appmodel.BindingConflict, appmodel.RevisionConflict, appmodel.QueueHeld, appmodel.CancelTooLate, appmodel.AlreadyCancelled, appmodel.StorageUnknown, appmodel.ResetRequired, appmodel.CapacityExceeded:
		return string(e.Code), true
	}
	return "", false
}
func backendError(w io.Writer, err error) int {
	if code, ok := domain(err); ok {
		return failure(w, "domain_error", code, 1)
	}
	if errors.Is(err, ErrUnavailable) {
		return failure(w, "observation_error", "unavailable", 1)
	}
	return failure(w, "observation_error", "unknown", 1)
}

// Run executes one invocation. Machine responses go to out; help and completion
// are static text. A failed or short output write returns 3 and is not retried.
// The caller must arrange cancellation of any in-flight stream I/O if needed.
func Run(ctx context.Context, backend Backend, args []string, in io.Reader, out io.Writer) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		n, e := io.WriteString(out, help)
		if e != nil || n != len(help) {
			return 3
		}
		return 0
	}
	if len(args) == 2 && args[0] == "completion" {
		if s, ok := completions[args[1]]; ok {
			n, e := io.WriteString(out, s)
			if e != nil || n != len(s) {
				return 3
			}
			return 0
		}
	}
	if len(args) < 2 || args[0] != "command" {
		return failure(out, "domain_error", "invalid_input", 2)
	}
	flags := map[string]string{}
	for i := 2; i < len(args); i += 2 {
		if i+1 >= len(args) || !strings.HasPrefix(args[i], "--") || flags[args[i]] != "" || args[i+1] == "" {
			return failure(out, "domain_error", "invalid_input", 2)
		}
		flags[args[i]] = args[i+1]
	}
	if flags["--format"] != "json" {
		return failure(out, "domain_error", "invalid_input", 2)
	}
	switch args[1] {
	case "submit":
		if len(flags) != 2 || flags["--file"] == "" {
			return failure(out, "domain_error", "invalid_input", 2)
		}
		if ctx.Err() != nil {
			return failure(out, "observation_error", "unavailable", 1)
		}
		reader := in
		if flags["--file"] != "-" {
			f, err := os.Open(flags["--file"])
			if err != nil {
				return failure(out, "domain_error", "invalid_input", 2)
			}
			defer f.Close()
			reader = f
		}
		if reader == nil {
			return failure(out, "domain_error", "invalid_input", 2)
		}
		b, err := io.ReadAll(io.LimitReader(reader, appmodel.MaxWireBytes+1))
		if err != nil || len(b) > appmodel.MaxWireBytes {
			return failure(out, "domain_error", "invalid_input", 2)
		}
		e, err := appmodel.Decode(b)
		if err != nil {
			if c, ok := domain(err); ok {
				return failure(out, "domain_error", c, 2)
			}
			return failure(out, "domain_error", "invalid_input", 2)
		}
		if ctx.Err() != nil || backend == nil {
			return failure(out, "observation_error", "unavailable", 1)
		}
		r, err := backend.Submit(ctx, e)
		if err != nil {
			return backendError(out, err)
		}
		if !validKey(r.Command) || r.Command.CommandID != e.CommandID || !validID(string(r.AdmittedRevision)) || len(r.Cursor) == 0 || len(r.Cursor) > maxCursorBytes || !utf8.ValidString(r.Cursor) {
			return failure(out, "observation_error", "unknown", 1)
		}
		return emit(out, result{Kind: "receipt", Command: wireKey(r.Command), Digest: hex.EncodeToString(r.Digest[:]), AdmittedRevision: r.AdmittedRevision, Cursor: r.Cursor}, 0)
	case "show":
		k := appmodel.CommandKey{Authority: flags["--authority"], EnrolledClientID: flags["--client"], CommandID: appmodel.CommandID(flags["--id"])}
		if len(flags) != 4 || !validKey(k) {
			return failure(out, "domain_error", "invalid_input", 2)
		}
		if ctx.Err() != nil || backend == nil {
			return failure(out, "observation_error", "unavailable", 1)
		}
		s, err := backend.Show(ctx, k)
		if err != nil {
			return backendError(out, err)
		}
		if s.Command != k || !validID(string(s.Revision)) || (s.State != appmodel.Queued && s.State != appmodel.Held && s.State != appmodel.Cancelled && s.State != appmodel.Applied) {
			return failure(out, "observation_error", "unknown", 1)
		}
		return emit(out, result{Kind: "status", Command: wireKey(s.Command), Revision: s.Revision, State: s.State}, 0)
	default:
		return failure(out, "domain_error", "unsupported_capability", 2)
	}
}
