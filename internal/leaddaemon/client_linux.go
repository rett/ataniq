package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"ataniq/internal/clientapi"
)

// DefaultStatusTimeout bounds one status call, dial included.
const DefaultStatusTimeout = 10 * time.Second

// StatusOptions are the inputs of [RunStatus].
type StatusOptions struct {
	// Socket is the socket path. Required.
	Socket string
	// JSON asks for the status result as JSON instead of text lines.
	JSON bool
	// Timeout bounds the whole call; default [DefaultStatusTimeout].
	Timeout time.Duration
}

// RunStatus asks the daemon at opts.Socket for its status, prints it to
// stdout and returns the exit code: [ExitOK] when the daemon answered,
// [ExitNoDaemon] when nothing answers at the socket (missing or refused),
// [ExitUnauthorized] when the daemon refused this user, [ExitProtocol] for an
// answer that is not valid or an error the daemon answered, and
// [ExitFailure] for anything else (a timeout, for one). Messages go to
// stderr, one line each.
//
// Every string the daemon sent reaches the output escaped (see [SafeText]);
// a hostile or faulty daemon cannot put a control sequence on the terminal.
func RunStatus(ctx context.Context, opts StatusOptions, stdout, stderr io.Writer) int {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultStatusTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c, err := clientapi.Dial(ctx, opts.Socket)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTDIR):
			fmt.Fprintf(stderr, "ataniqd: no daemon answers at %s\n", SafeText(opts.Socket))
			return ExitNoDaemon
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintf(stderr, "ataniqd: no answer from %s in %s\n", SafeText(opts.Socket), timeout)
			return ExitFailure
		}
		fmt.Fprintf(stderr, "ataniqd: cannot connect to %s: %s\n", SafeText(opts.Socket), SafeText(err.Error()))
		return ExitFailure
	}
	defer c.Close()

	version := ""
	if !opts.JSON {
		hello, err := c.Hello(ctx)
		if err != nil {
			return statusFailure(err, stderr)
		}
		version = hello.Version
	}
	res, err := c.Status(ctx)
	if err != nil {
		return statusFailure(err, stderr)
	}
	if opts.JSON {
		b, err := asciiJSON(res)
		if err != nil {
			fmt.Fprintf(stderr, "ataniqd: cannot encode the status: %s\n", SafeText(err.Error()))
			return ExitFailure
		}
		stdout.Write(b)
		return ExitOK
	}
	writeStatus(stdout, version, res)
	return ExitOK
}

// statusFailure prints the failure of a call and returns its exit code.
func statusFailure(err error, stderr io.Writer) int {
	var ce *clientapi.Error
	switch {
	case errors.As(err, &ce):
		if ce.Code == clientapi.CodeUnauthorized {
			fmt.Fprintln(stderr, "ataniqd: the daemon refused this user (unauthorized)")
			return ExitUnauthorized
		}
		fmt.Fprintf(stderr, "ataniqd: the daemon answered an error: %s\n", SafeText(ce.Error()))
		return ExitProtocol
	case errors.Is(err, clientapi.ErrBadResponse), errors.Is(err, clientapi.ErrResponseTooLarge):
		fmt.Fprintf(stderr, "ataniqd: protocol error: %s\n", SafeText(err.Error()))
		return ExitProtocol
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "ataniqd: the daemon did not answer in time")
		return ExitFailure
	}
	fmt.Fprintf(stderr, "ataniqd: protocol error: %s\n", SafeText(err.Error()))
	return ExitProtocol
}

func writeStatus(w io.Writer, version string, res clientapi.StatusResult) {
	d := res.Daemon
	reconcile := "incomplete"
	if d.LastReconcileCompleted {
		reconcile = "completed"
	}
	at := "never"
	if d.LastReconcileAt != nil {
		at = d.LastReconcileAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(w, "daemon version=%s pid=%d started=%s revision=%d boot=%s reconcile=%s reconcile_at=%s\n",
		tok(version), d.PID, d.StartedAt.UTC().Format(time.RFC3339), d.RegistryRevision, tok(d.BootID), reconcile, at)
	for _, l := range res.Leads {
		fmt.Fprintf(w, "role=%s kind=%s state=%s reason=%s epoch=%d desired=%s manager=%s",
			tok(l.Role), tok(l.Kind), tok(l.State), tok(l.ReasonCode), l.Epoch, tok(l.Desired), tok(l.Manager))
		if l.Kind == clientapi.KindCoordinator {
			fmt.Fprintf(w, " parent=%s", tok(l.Parent))
			if l.Instance != nil {
				fmt.Fprintf(w, " instance=%d", *l.Instance)
			}
		}
		if l.ReasonDetail != "" {
			fmt.Fprintf(w, " detail=%s", quote(l.ReasonDetail))
		}
		fmt.Fprintln(w)
	}
}

// asciiJSON encodes v as indented JSON with DEL and every non-ASCII character
// written as a \u escape, so that no byte of it is a control character of a terminal
// (the C1 controls and the bidirectional overrides among them) and the text
// decodes to the same value.
func asciiJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	in := buf.Bytes()
	out := make([]byte, 0, len(in))
	for len(in) > 0 {
		r, n := utf8.DecodeRune(in)
		switch {
		case r == 0x7f:
			out = append(out, `\u007f`...)
		case r < utf8.RuneSelf:
			out = append(out, in[0])
		case r == utf8.RuneError && n == 1:
			out = append(out, `\ufffd`...)
		case r >= 0x10000:
			r1 := 0xd800 + (r-0x10000)>>10
			r2 := 0xdc00 + (r-0x10000)&0x3ff
			out = append(out, fmt.Sprintf(`\u%04x\u%04x`, r1, r2)...)
		default:
			out = append(out, fmt.Sprintf(`\u%04x`, r)...)
		}
		in = in[n:]
	}
	return out, nil
}

// SafeText returns s with everything that is not a printable ASCII character
// written as an escape (\n, \r, \t, \xNN for a byte that is not valid UTF-8 or
// a control character below 0x20 or 0x7f, \uNNNN and \UNNNNNNNN for every other
// character), and a backslash doubled,
// so the result can be printed to a terminal or a log whatever s holds.
func SafeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += n
	}
	return b.String()
}

// quote returns s escaped as [SafeText] does, in double quotes.
func quote(s string) string {
	return `"` + strings.ReplaceAll(SafeText(s), `"`, `\"`) + `"`
}

// tok returns s escaped, bare when it is a plain word and quoted otherwise,
// so that a line of key=value pairs stays readable and cannot be split by a
// value.
func tok(s string) string {
	if s == "" {
		return `""`
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._/:@+-", c) >= 0) {
			return quote(s)
		}
	}
	return s
}
