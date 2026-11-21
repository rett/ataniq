//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
)

// ReplySchema names a reply file.
const ReplySchema = "ataniq.owner-reply/1"

// replySuffix ends the name of a reply file, and requestSuffix the name of a
// request file in the drop folder.
const (
	replySuffix   = ".reply"
	requestSuffix = ".request"
)

// maxReplyBytes caps a reply file: its fields are short host-written words.
const maxReplyBytes = 4 << 10

// FileMailbox is the only mailbox this build has: a reply is a file in the
// drop folder, next to the request it answers, named after the request id. It
// is not a reply into a lead's mailbox, which is out of scope. The folder is
// the host's: a lead's sandbox cannot write it.
//
// A reply is written once. The first reply for a request id stays and a
// second is dropped without an error, since the service may answer again
// after a restart.
type FileMailbox struct {
	dir string
}

var _ ownerchannel.Mailbox = (*FileMailbox)(nil)

// NewFileMailbox returns the mailbox of the folder, which must exist, be a
// real folder (not a symbolic link) owned by the caller, and not be reachable
// by the group or by others.
func NewFileMailbox(dir string) (*FileMailbox, error) {
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	return &FileMailbox{dir: dir}, nil
}

// checkPrivateDir refuses a folder that is not the caller's own private one.
func checkPrivateDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("hostadapter: the folder is not a clean absolute path")
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("hostadapter: the folder: %w", err)
	}
	if !st.IsDir() {
		return errors.New("hostadapter: the folder is not a real folder")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return errors.New("hostadapter: the folder is reachable by the group or by others")
	}
	if !ownedByMe(st) {
		return errors.New("hostadapter: the folder is not the caller's")
	}
	return nil
}

// replyFile is what a reply file holds. Every field is host-written, and none
// is a secret: there is no confirm code and no plan token in a reply.
type replyFile struct {
	Schema    string `json:"schema"`
	RequestID string `json:"request_id"`
	PendingID string `json:"pending_id,omitempty"`
	State     string `json:"state,omitempty"`
	Code      string `json:"code,omitempty"`
	Line      string `json:"line"`
}

// ReplyPath returns the path of the reply file of a request id in the
// mailbox's folder.
func (m *FileMailbox) ReplyPath(requestID string) string {
	return filepath.Join(m.dir, requestID+replySuffix)
}

// Reply implements [ownerchannel.Mailbox]. A request id that fails the
// grammar names no file and is refused.
func (m *FileMailbox) Reply(_ context.Context, r ownerchannel.Reply) error {
	if !record.ValidRequestID(r.RequestID) {
		return errors.New("hostadapter: the reply names no valid request id")
	}
	b, err := json.Marshal(replyFile{
		Schema: ReplySchema, RequestID: r.RequestID, PendingID: r.PendingID,
		State: string(r.State), Code: string(r.Code), Line: r.Line,
	})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > maxReplyBytes {
		return errors.New("hostadapter: the reply is larger than a reply file may be")
	}
	return writeOnce(m.dir, r.RequestID+replySuffix, b)
}

// writeOnce writes a file that does not exist yet and never replaces one:
// the bytes go to a temporary name, are synced, and are linked to the name,
// which fails when the name is taken (the first writer stays).
func writeOnce(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, filepath.Join(dir, name)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
