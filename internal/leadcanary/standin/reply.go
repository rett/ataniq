//go:build linux && ataniq_canary

package standin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// maxListed bounds how many names of the reply folder the stand-in reads to
// count its own replies; a folder that holds more gets no reply (it is
// best-effort, and the daemon stops such a chat anyway).
const maxListed = 64

// replyHook, a test's hook, runs in writeReply once the folder is held and
// listed, before the reply is written.
var replyHook func()

// writeReply writes one reply, "Stand-in: handled <id>.", to the reply folder
// of home, and returns the sequence number it used (seq is the first number
// to try). It writes nothing, and says so with an error, when home is empty,
// when the folder is missing, is not a folder (a link is not), cannot be
// read or written, holds more than maxListed names, or already holds
// MaxWaitingReplies of the stand-in's own replies. The folder is opened once,
// without following a link, and every later step works below that one
// descriptor: a folder replaced meanwhile (a turn-on makes a new one) is
// never reached by name again. It never makes, changes or removes the folder,
// or any file but its own: the text goes first to a dot-name, which is closed
// and then renamed without replacing to "<seq>-standin.txt".
func writeReply(home string, seq int, id string) (int, error) {
	if home == "" {
		return seq, errors.New("standin: no home")
	}
	if !validReplyID(id) {
		return seq, errors.New("standin: no valid commit id")
	}
	dfd, err := unix.Open(filepath.Join(home, OutboxName), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR:
		return seq, errors.New("standin: the reply folder is not a folder")
	case err != nil:
		return seq, fmt.Errorf("standin: reply folder: %w", err)
	}
	defer unix.Close(dfd)
	names, more, err := readFolderNames(dfd, maxListed)
	if err != nil {
		return seq, fmt.Errorf("standin: reply folder: %w", err)
	}
	if more {
		return seq, errors.New("standin: the reply folder holds too many names")
	}
	waiting := 0
	for _, name := range names {
		if isOwnReply(name) {
			waiting++
		}
	}
	if waiting >= MaxWaitingReplies {
		return seq, errors.New("standin: replies are waiting")
	}
	if replyHook != nil {
		replyHook()
	}
	text := replyText(id)
	fd, err := unix.Openat(dfd, replyTmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return seq, fmt.Errorf("standin: reply: %w", err)
	}
	f := os.NewFile(uintptr(fd), replyTmpName)
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = unix.Unlinkat(dfd, replyTmpName, 0)
		return seq, fmt.Errorf("standin: reply: %w", err)
	}
	for ; seq <= replySeqMax; seq++ {
		err = unix.Renameat2(dfd, replyTmpName, dfd, fmt.Sprintf("%04d%s", seq, ReplySuffix), unix.RENAME_NOREPLACE)
		if err == nil {
			return seq + 1, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			break
		}
	}
	_ = unix.Unlinkat(dfd, replyTmpName, 0)
	return seq, fmt.Errorf("standin: reply: %w", err)
}

// readFolderNames reads at most max names of the folder dfd (without "." and
// ".."), and reports whether it holds more.
func readFolderNames(dfd int, max int) ([]string, bool, error) {
	names := []string{}
	buf := make([]byte, 8192)
	for {
		n, err := unix.ReadDirent(dfd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if n <= 0 {
			return names, false, nil
		}
		_, _, names = unix.ParseDirent(buf[:n], max+1-len(names), names)
		if len(names) > max {
			return names[:max], true, nil
		}
	}
}
