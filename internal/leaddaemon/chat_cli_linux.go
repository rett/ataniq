//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadwire"
)

// ChatCallTimeout bounds one call of the chat command, dial included. A turn-on
// clears the lead's reply folder first, which takes a moment at most.
const ChatCallTimeout = 30 * time.Second

// ChatCall is one call of the desk's chat command, "ataniqd chat".
type ChatCall struct {
	// Socket is the daemon's socket path. Required.
	Socket string
	// Op is leadwire.OpChatList or leadwire.OpChatSet. Required.
	Op string
	// Key and Chat are the lead's key and the switch, off or read, for
	// chat.set.
	Key, Chat string
	// Timeout bounds the call; default [ChatCallTimeout].
	Timeout time.Duration
}

// RunChat calls chat.list or chat.set of the daemon at c.Socket, prints the
// result as one JSON line to stdout and returns the exit code: [ExitOK] when
// the daemon answered a result; [ExitRefused] for a key that is not a lead of
// the daemon, a key the seat cannot hold and send, which is not built;
// [ExitUnavailable] when the lead's chat cannot be used; and the codes of
// [RunCanary] for the rest. Messages go to stderr, one line each, escaped.
func RunChat(ctx context.Context, c ChatCall, stdout, stderr io.Writer) int {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ChatCallTimeout
	}
	var params, result any
	switch c.Op {
	case leadwire.OpChatList:
		result = new(leadwire.ChatListResult)
	case leadwire.OpChatSet:
		params, result = leadwire.ChatSetParams{Key: c.Key, Chat: c.Chat}, new(leadwire.ChatLead)
	default:
		fmt.Fprintf(stderr, "ataniqd: %s is not a chat operation\n", SafeText(c.Op))
		return ExitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cl, err := clientapi.Dial(ctx, c.Socket)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTDIR):
			fmt.Fprintf(stderr, "ataniqd: no daemon answers at %s\n", SafeText(c.Socket))
			return ExitNoDaemon
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintf(stderr, "ataniqd: no answer from %s in %s\n", SafeText(c.Socket), timeout)
			return ExitFailure
		}
		fmt.Fprintf(stderr, "ataniqd: cannot connect to %s: %s\n", SafeText(c.Socket), SafeText(err.Error()))
		return ExitFailure
	}
	defer cl.Close()
	if err := cl.Call(ctx, c.Op, params, result); err != nil {
		var ce *clientapi.Error
		if errors.As(err, &ce) {
			switch string(ce.Code) {
			case leadwire.CodeChatSendNotBuilt, leadwire.CodeChatKeyUnrepresentable:
				fmt.Fprintf(stderr, "ataniqd: %s refused: %s\n", c.Op, SafeText(ce.Message))
				return ExitRefused
			case string(clientapi.CodeUnsupportedOperation):
				fmt.Fprintln(stderr, "ataniqd: the daemon has no chat: it does not run in scratch mode, or is of an older version")
				return ExitProtocol
			}
		}
		return canaryFailure(c.Op, err, stderr)
	}
	line, err := asciiLine(result)
	if err != nil {
		fmt.Fprintf(stderr, "ataniqd: cannot encode the result: %s\n", SafeText(err.Error()))
		return ExitFailure
	}
	stdout.Write(line)
	return ExitOK
}
