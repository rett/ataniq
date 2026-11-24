//go:build linux && ataniq_canary

package standin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The stand-in's lead reply: after a commit, one short plain-text file in the
// lead's reply folder (the outbox, see docs/scratch-leads.md "Lead replies"),
// so that the attended proof of the outbox bind runs with no AI.
const (
	// OutboxName is the reply folder in the account's home. The daemon makes
	// it; the stand-in never does.
	OutboxName = ".ataniq-replies"
	// ReplySuffix ends the name of every reply the stand-in writes, and is
	// how it tells its own files from any other.
	ReplySuffix = "-standin.txt"
	// MaxWaitingReplies is how many of its own replies may be waiting in the
	// folder; with that many there, it writes no new one.
	MaxWaitingReplies = 3
	// MaxReplyBytes bounds the text of a reply.
	MaxReplyBytes = 200

	replyIDLen   = 7
	replyTmpName = ".standin-reply.tmp"
	replySeqMax  = 9999
)

// replyText is the text of the reply for one handled commit, whose short id
// is id.
func replyText(id string) string {
	return "Stand-in: handled " + id + "."
}

// validReplyID accepts an id of 1 to 7 lower-case hexadecimal digits, so that
// no other text reaches a reply.
func validReplyID(id string) bool {
	if len(id) == 0 || len(id) > replyIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// isOwnReply reports whether name is the name of a reply the stand-in wrote:
// digits, then ReplySuffix.
func isOwnReply(name string) bool {
	digits, ok := strings.CutSuffix(name, ReplySuffix)
	if !ok || digits == "" {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// writeReply writes one reply, "Stand-in: handled <id>.", to the reply folder
// of home, and returns the sequence number it used (seq is the first number
// to try). It writes nothing, and says so with an error, when home is empty,
// when the folder is missing, is not a folder (a link is not), cannot be
// read or written, or already holds MaxWaitingReplies of the stand-in's own
// replies. It never makes, changes or removes the folder, or any file but its
// own: the text goes first to a dot-name, which is closed and then renamed
// without replacing to "<seq>-standin.txt".
func writeReply(home string, seq int, id string) (int, error) {
	if home == "" {
		return seq, errors.New("standin: no home")
	}
	if !validReplyID(id) {
		return seq, errors.New("standin: no valid commit id")
	}
	dir := filepath.Join(home, OutboxName)
	st, err := os.Lstat(dir)
	if err != nil {
		return seq, fmt.Errorf("standin: reply folder: %w", err)
	}
	if !st.IsDir() {
		return seq, errors.New("standin: the reply folder is not a folder")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return seq, fmt.Errorf("standin: reply folder: %w", err)
	}
	waiting := 0
	for _, e := range entries {
		if isOwnReply(e.Name()) {
			waiting++
		}
	}
	if waiting >= MaxWaitingReplies {
		return seq, errors.New("standin: replies are waiting")
	}
	text := replyText(id)
	tmp := filepath.Join(dir, replyTmpName)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return seq, fmt.Errorf("standin: reply: %w", err)
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return seq, fmt.Errorf("standin: reply: %w", err)
	}
	for ; seq <= replySeqMax; seq++ {
		final := filepath.Join(dir, fmt.Sprintf("%04d%s", seq, ReplySuffix))
		err = unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE)
		if err == nil {
			return seq + 1, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			break
		}
	}
	os.Remove(tmp)
	return seq, fmt.Errorf("standin: reply: %w", err)
}
