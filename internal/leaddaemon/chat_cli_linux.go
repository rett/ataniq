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

// QuarantineFullLine is what "ataniqd chat set KEY read" prints when the
// turn-on is refused because the lead's quarantine is full.
const QuarantineFullLine = "ataniqd: chat set: the lead's quarantine is full (a turn-on needs one of its four names free); stop the lead, run ataniqd chat purge KEY --config CONFIG (or empty <state root>/leads/<layout role>/quarantine by hand), then turn chat on again"

// ChatCall is one call of the desk's chat command, "ataniqd chat".
type ChatCall struct {
	// Socket is the daemon's socket path. Required.
	Socket string
	// Op is leadwire.OpChatList, leadwire.OpChatSet or
	// leadwire.OpChatPurge. Required.
	Op string
	// Key is the lead's key, for chat.set and chat.purge; Chat is the
	// switch, off or read, for chat.set.
	Key, Chat string
	// Timeout bounds the call; default [ChatCallTimeout].
	Timeout time.Duration
}

// RunChat calls chat.list, chat.set or chat.purge of the daemon at c.Socket,
// prints the result as one JSON line to stdout and returns the exit code:
// [ExitOK] when the daemon answered a result; [ExitRefused] for a key that is
// not a lead of the daemon, a key the seat cannot hold, send, which is not
// built, and a purge of a lead that is not proven stopped; [ExitUnavailable] when the lead's chat cannot be used, and for
// a turn-on refused because the lead's quarantine is full (with the fixed
// line [QuarantineFullLine]); and the codes of [RunCanary] for the rest.
// Messages go to stderr, one line each, escaped.
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
	case leadwire.OpChatPurge:
		params, result = leadwire.ChatPurgeParams{Key: c.Key}, new(leadwire.ChatPurgeResult)
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
			case leadwire.CodeChatSendNotBuilt, leadwire.CodeChatKeyUnrepresentable, leadwire.CodeChatLeadNotStopped:
				fmt.Fprintf(stderr, "ataniqd: %s refused: %s\n", c.Op, SafeText(ce.Message))
				return ExitRefused
			case leadwire.CodeChatQuarantineFull:
				fmt.Fprintln(stderr, QuarantineFullLine)
				return ExitUnavailable
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
