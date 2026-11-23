package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"time"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

// timeLayout is the one form of a time in a thread file, as in a brief.
const timeLayout = "2006-01-02T15:04:05Z"

// maxDepth is how deep a thread file nests: the thread, and the brief with its
// own depth.
const maxDepth = 10

// fileName matches the name of a thread's file, and tmpPrefix starts the name
// of the temporary file a write goes through.
var fileName = regexp.MustCompile(`^[0-9a-f]{16}\.json$`)

const (
	fileSuffix = ".json"
	tmpPrefix  = ".w-"
	tmpSuffix  = ".tmp"
)

func fileOf(id string) string { return id + fileSuffix }
func tmpOf(id string) string  { return tmpPrefix + id + tmpSuffix }

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// encodeRecord writes a thread in its file form: the canonical JSON of its
// members and a digest of the rest, which is SHA-256 of the canonical JSON of
// the members without it.
func encodeRecord(r record) ([]byte, error) {
	bb, err := brief.Encode(r.brief)
	if err != nil {
		return nil, err
	}
	bv, err := canon.Parse(bb, maxDepth)
	if err != nil {
		return nil, err
	}
	ms := []canon.Member{
		canon.M("brief", bv),
		canon.M("catalog", canon.Str(r.catalog)),
		canon.M("created", canon.Str(r.created.Format(timeLayout))),
		canon.M("id", canon.Str(r.id)),
		canon.M("schema", canon.Str(Schema)),
		canon.M("state", canon.Str(string(r.state))),
		canon.M("updated", canon.Str(r.updated.Format(timeLayout))),
	}
	if r.frozen != "" {
		ms = append(ms, canon.M("brief_digest", canon.Str(r.frozen)))
	}
	body, err := canon.Encode(canon.Obj(ms...))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	ms = append(ms, canon.M("digest", canon.Str(hex.EncodeToString(sum[:]))))
	out, err := canon.Encode(canon.Obj(ms...))
	if err != nil {
		return nil, err
	}
	if len(out) > maxFileBytes {
		return nil, errors.New("the thread is larger than its file may be")
	}
	return out, nil
}

// errBad is the error of a file that does not pass its checks. Its text names
// the check by its place in the form and never holds a byte of the file.
type errBad struct{ why string }

func (e *errBad) Error() string { return e.why }

func bad(why string) error { return &errBad{why: why} }

// decodeRecord reads a thread file strictly. name is the file's name, whose
// stem is the thread's id.
func decodeRecord(name string, data []byte) (record, error) {
	if len(data) > maxFileBytes {
		return record{}, bad("the file is larger than a thread file may be")
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return record{}, bad("the file is not strict JSON")
	}
	// Only the canonical form is read: what the store wrote.
	again, err := canon.Encode(v)
	if err != nil || !bytes.Equal(again, data) {
		return record{}, bad("the file is not in the canonical form")
	}
	m, err := canon.Fields(v, "thread", []string{"brief", "catalog", "created", "digest", "id", "schema", "state", "updated"}, []string{"brief_digest"})
	if err != nil {
		return record{}, bad(err.Error())
	}
	str := func(key string) (string, error) { s, err := canon.AsString(m[key], "thread."+key); return s, err }
	schema, err := str("schema")
	if err != nil || schema != Schema {
		return record{}, bad("the schema is not the thread's")
	}
	var r record
	if r.id, err = str("id"); err != nil || !isHex(r.id, 16) || fileOf(r.id) != name {
		return record{}, bad("the id is not the file's name")
	}
	state, err := str("state")
	if err != nil || !State(state).valid() {
		return record{}, bad("the state is not open, finished or discarded")
	}
	r.state = State(state)
	if r.catalog, err = str("catalog"); err != nil || !isHex(r.catalog, 64) {
		return record{}, bad("the catalog digest is not 64 hexadecimal digits")
	}
	for _, tm := range []struct {
		key string
		to  *time.Time
	}{{"created", &r.created}, {"updated", &r.updated}} {
		s, err := str(tm.key)
		if err != nil {
			return record{}, bad(err.Error())
		}
		t, err := time.Parse(timeLayout, s)
		if err != nil || t.Format(timeLayout) != s {
			return record{}, bad("a time is not in UTC to the second")
		}
		*tm.to = t
	}
	if r.updated.Before(r.created) {
		return record{}, bad("the thread was updated before it was made")
	}
	bb, err := canon.Encode(m["brief"])
	if err != nil {
		return record{}, bad("the brief does not encode")
	}
	if r.brief, err = brief.Decode(bb); err != nil {
		return record{}, bad("the brief does not pass its own checks")
	}
	if r.brief.Catalog != r.catalog {
		return record{}, bad("the brief was started under another catalog than the thread")
	}
	bd, hasDigest := m["brief_digest"]
	switch {
	case r.state == StateFinished:
		if !hasDigest {
			return record{}, bad("a finished thread has no brief digest")
		}
		if r.frozen, err = canon.AsString(bd, "thread.brief_digest"); err != nil {
			return record{}, bad(err.Error())
		}
		if sum, err := brief.Digest(r.brief); err != nil || sum != r.frozen {
			return record{}, bad("the brief does not match the digest it was frozen with")
		}
	case hasDigest:
		return record{}, bad("a thread that is not finished has a brief digest")
	}
	// The digest of the file's own content.
	rest := make([]canon.Member, 0, len(v.Obj))
	for _, mem := range v.Obj {
		if mem.Key != "digest" {
			rest = append(rest, mem)
		}
	}
	body, err := canon.Encode(canon.Obj(rest...))
	if err != nil {
		return record{}, bad("the file does not encode")
	}
	sum := sha256.Sum256(body)
	if got, _ := str("digest"); got != hex.EncodeToString(sum[:]) {
		return record{}, bad("the digest of the file does not match its content")
	}
	return r, nil
}

// readFile reads one thread file of the folder, through the root: a regular
// file, not a link, of the daemon's user, with no mode bit but those of
// 0600, of at most the cap.
func readFile(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := checkFileInfo(fi); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	held, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, held) {
		return nil, bad("the file was replaced while it was opened")
	}
	if err := checkFileInfo(held); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func checkFileInfo(fi os.FileInfo) error {
	switch {
	case !fi.Mode().IsRegular():
		return bad("the file is not a regular file")
	case fi.Mode().Perm()&^0o600 != 0:
		return bad("the file is open to group or other")
	case !ownedByUs(fi):
		return bad("the file is not the daemon user's")
	case fi.Size() > maxFileBytes:
		return bad("the file is larger than a thread file may be")
	}
	return nil
}

// writeFile writes a thread's file atomically: a temporary file of the same
// folder, synced, renamed over the thread's file, and the folder synced.
func writeFile(root *os.Root, id string, data []byte) error {
	tmp := tmpOf(id)
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, fileOf(id)); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return syncDir(root)
}

// syncDir makes the folder's entries durable.
func syncDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
