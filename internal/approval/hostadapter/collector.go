//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
)

// collectedDir is the folder, inside the drop folder, that a request file and
// its note are moved into when they are taken.
const collectedDir = "collected"

// noteSuffix ends the name of a note file, which travels beside the request.
const noteSuffix = ".note"

// requestName is the name of a request file: the request's id and ".request".
// A file with any other name is not a request and is left alone.
var requestName = regexp.MustCompile(`^(rq-[a-z0-9-]{1,48})\.request$`)

// SubmitFunc hands a collected request to the service: [ownerchannel.Service]'s
// Submit is one.
type SubmitFunc func(ctx context.Context, in ownerchannel.Collected) (ownerchannel.RequestStatus, error)

// Collector is a file-drop collector. It exists for the scratch pass only: a
// request is a file named <request id>.request in the drop folder, with an
// optional note in <request id>.note, and the real spool is out of scope.
//
// A file is read with the caps of the record package, moved into the folder
// "collected" so that it is taken once, and handed to the service. A request
// that cannot be taken is answered with a reply file beside it and never
// reaches the service: a request that does not decode, whose id is not the
// file's name, that is too large, or whose feature name is outside the
// grammar (M5, which the decoder of the request holds). The reply file is the
// only reply this build writes (L17); it is the mailbox's own, so the
// service's replies are the same kind of file.
//
// The drop folder is the host's. The collector does not stop a process that
// can write it from dropping a file, so it is private to the user; what the
// files hold is read as untrusted all the same.
type Collector struct {
	mail    *FileMailbox
	project string
	submit  SubmitFunc
	now     func() time.Time
	log     func(string)
}

// NewCollector returns the collector of the mailbox's folder for one project.
// now is the clock of the time a request is collected, and log, when set,
// receives one host-written line for each request that could not be handed to
// the service.
func NewCollector(mail *FileMailbox, project string, submit SubmitFunc, now func() time.Time, log func(string)) (*Collector, error) {
	if mail == nil || submit == nil {
		return nil, errors.New("hostadapter: the collector needs a mailbox and a service")
	}
	if project == "" {
		return nil, errors.New("hostadapter: the collector needs a project")
	}
	if now == nil {
		now = time.Now
	}
	if err := os.Mkdir(filepath.Join(mail.dir, collectedDir), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	if err := checkPrivateDir(filepath.Join(mail.dir, collectedDir)); err != nil {
		return nil, err
	}
	return &Collector{mail: mail, project: project, submit: submit, now: now, log: log}, nil
}

func (c *Collector) logf(format string, a ...any) {
	if c.log != nil {
		c.log(fmt.Sprintf(format, a...))
	}
}

// Scan takes every request file that is in the folder now, in the order of
// their names, and returns how many it took. A request that already has a
// reply is left where it is. Scan returns when each was handed over or
// answered, which for a request that is verified is when the verification has
// ended, or when ctx ends.
func (c *Collector) Scan(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(c.mail.dir)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, e := range entries {
		if m := requestName.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
			ids = append(ids, m[1])
		}
	}
	sort.Strings(ids)
	taken := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return taken, err
		}
		if _, err := os.Lstat(c.mail.ReplyPath(id)); err == nil {
			continue
		}
		c.take(ctx, id)
		taken++
	}
	return taken, nil
}

// refuse answers a request file with a reply that has the code and no state.
func (c *Collector) refuse(ctx context.Context, id string, code record.Code, detail string) {
	if err := c.mail.Reply(ctx, ownerchannel.Reply{Project: c.project, RequestID: id, Code: code, Line: detail}); err != nil {
		c.logf("a reply file could not be written for a refused request: %s", err)
	}
}

// take reads, moves and hands over one request file.
func (c *Collector) take(ctx context.Context, id string) {
	reqPath := filepath.Join(c.mail.dir, id+requestSuffix)
	notePath := filepath.Join(c.mail.dir, id+noteSuffix)
	req, err := readSmall(reqPath, record.MaxRequestBytes)
	if err != nil {
		c.refuse(ctx, id, readCode(err), "the request file could not be read as a request")
		c.keep(reqPath, id+requestSuffix)
		return
	}
	note, err := readSmall(notePath, record.MaxNoteBytes)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.refuse(ctx, id, readCode(err), "the note file could not be read as a note")
		c.keep(reqPath, id+requestSuffix)
		c.keep(notePath, id+noteSuffix)
		return
	}
	// The files are moved first, so that a request is taken once whatever
	// happens next. A name taken before is a second request with the id.
	if err := c.move(reqPath, id+requestSuffix); err != nil {
		// The reply file of this id belongs to the request taken first,
		// whose outcome may still come and which is written once: a second
		// file never answers the id. It is set aside, unanswered.
		c.keepAside(reqPath, id+requestSuffix)
		c.keepAside(notePath, id+noteSuffix)
		c.logf("a second request file for an id taken before was set aside, unanswered")
		return
	}
	if note != nil {
		_ = c.move(notePath, id+noteSuffix)
	}
	// DecodeRequest holds the grammar of the feature name (M5), the one
	// lead-chosen string that may stand in the chrome of a card: a name
	// outside it is refused feature-name here, before anything is built.
	rq, err := record.DecodeRequest(req)
	if err != nil {
		code, ok := record.CodeOf(err)
		if !ok {
			code = record.CodeRequestInvalid
		}
		c.refuse(ctx, id, code, detailOf(err))
		return
	}
	if rq.RequestID != id {
		c.refuse(ctx, id, record.CodeRequestInvalid, "the name of the request file is not the request's id")
		return
	}
	if _, err := c.submit(ctx, ownerchannel.Collected{Project: c.project, Request: req, Note: record.Text(note), At: c.now()}); err != nil {
		c.logf("a request could not be taken through to a card or a refusal: %s", oneLine(err.Error()))
	}
}

// detailOf is the host-written detail of a refusal, or a fixed line.
func detailOf(err error) string {
	var e *record.Error
	if errors.As(err, &e) && e.Detail != "" {
		return e.Detail
	}
	return "the request does not follow the record's rules"
}

// readCode is the code a file that could not be read is answered with.
func readCode(err error) record.Code {
	if errors.Is(err, errTooLarge) {
		return record.CodeRecordTooLarge
	}
	return record.CodeRequestInvalid
}

var errTooLarge = errors.New("hostadapter: the file is larger than its cap")

// move links the file into the collected folder under name and removes it
// from the drop folder. It fails when the name is taken. A crash between the
// link and the remove leaves the file in both folders; the next scan finds the
// name taken and sets the file aside, without a reply.
func (c *Collector) move(path, name string) error {
	if err := os.Link(path, filepath.Join(c.mail.dir, collectedDir, name)); err != nil {
		return err
	}
	return os.Remove(path)
}

// keep moves a file that was refused out of the drop folder, so that it is
// not read again; a name taken already leaves it where it is.
func (c *Collector) keep(path, name string) {
	if _, err := os.Lstat(path); err != nil {
		return
	}
	_ = c.move(path, name)
}

// maxAside is how many files of one name are set aside before the next is left
// where it is.
const maxAside = 1000

// keepAside moves a file whose name is taken in the collected folder out of the
// drop folder, under the name and ".dup-" and the first free number, a name
// that no request's and no note's name can have. A file that cannot be set
// aside is left where it is.
func (c *Collector) keepAside(path, name string) {
	if _, err := os.Lstat(path); err != nil {
		return
	}
	for n := 1; n <= maxAside; n++ {
		err := c.move(path, fmt.Sprintf("%s.dup-%d", name, n))
		if err == nil || !errors.Is(err, fs.ErrExist) {
			return
		}
	}
}

// readSmall reads a regular file of at most max bytes without following a
// symbolic link and without blocking on a pipe. A file that is not regular or
// is larger is refused; a missing file is [fs.ErrNotExist].
func readSmall(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("hostadapter: the file is not a regular file")
	}
	if st.Size() > max {
		return nil, errTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	if b == nil {
		b = []byte{}
	}
	return b, nil
}

// oneLine makes a one-line host-written text of s: control characters become
// spaces and the text is cut at 200 bytes on a character boundary.
func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	n := 0
	for _, r := range s {
		if record.IsControl(r) {
			r = ' '
		}
		if n += len(string(r)); n > 200 {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

// ownedByMe reports whether the file belongs to the user the process runs as.
func ownedByMe(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Getuid()
}
