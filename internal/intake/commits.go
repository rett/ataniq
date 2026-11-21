package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
)

// A commit is read as the raw object that the lead wrote, never through
// "git log". git log converts the text of a commit from the charset that the
// commit's own "encoding" header names, and that header is the lead's. The
// conversion would run in the owner's process, on a converter that the lead
// chose. So the commits are listed with rev-list, their sizes are checked
// with cat-file --batch-check, their objects are read with cat-file --batch,
// and the headers are parsed here, with fixed bounds. No text is converted:
// the fields are the stored bytes, and a commit that declares an encoding
// other than UTF-8 is refused (see [ErrCommitEncoding]).

// maxCommitField (in run.go) is the longest field of a commit: one header
// with its continuation lines, or the subject.
//
// maxIDLen is the longest object id, a SHA-256 one in hexadecimal.
//
// maxCommitObject is the longest commit object that is read, headers and
// message together. A longer one is [ErrLimit]; it is not read.
//
// commitBudget is the object bytes that all the commits of a review may
// have, for each commit on average. A review has this budget for each commit
// plus one maximal object, so a branch of many long messages is ErrLimit and
// the owner never holds gigabytes of lead text. With the default limit of
// 5000 commits it is about 40 MiB.
const (
	maxIDLen        = 64
	maxCommitObject = 2 * maxCommitField
	commitBudget    = 8 << 10
)

// ErrCommitEncoding means that a commit has an "encoding" header whose value
// is not UTF-8. The package never converts text, so such a commit is
// refused, with its id in the message; the lead has to commit again in
// UTF-8. The value is not UTF-8 even when it names a charset that git knows.
var ErrCommitEncoding = errors.New("intake: a commit declares an encoding other than UTF-8")

// ErrCommitFormat means that a commit object is not in the form the package
// reads: a header out of order or without a value, a second encoding header,
// an identity that is not "Name <address> seconds zone", or a header that is
// not terminated. The commit is refused, with its id in the message.
var ErrCommitFormat = errors.New("intake: a commit object is malformed")

// commits lists base..tip. The commit count is checked first with rev-list
// --count, so a branch over the limit is refused before its text is read.
func (r *runner) commits(ctx context.Context, base, tip string, lim Limits) ([]Commit, error) {
	n, err := r.count(ctx, base, tip)
	if err != nil {
		return nil, err
	}
	if n > lim.MaxCommits {
		return nil, fmt.Errorf("%w: %d commits, the limit is %d", ErrLimit, n, lim.MaxCommits)
	}
	if n == 0 {
		return nil, nil
	}
	ids, err := r.commitIDs(ctx, base, tip, n)
	if err != nil {
		return nil, err
	}
	sizes, err := r.commitSizes(ctx, ids)
	if err != nil {
		return nil, err
	}
	objects, err := r.commitObjects(ctx, ids, sizes)
	if err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, n)
	for i, id := range ids {
		c, err := parseCommit(id, objects[i])
		if err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// commitIDs lists the ids of base..tip, newest first as git log does. It
// wants exactly want of them. rev-list reads no message and converts
// nothing.
func (r *runner) commitIDs(ctx context.Context, base, tip string, want int) ([]string, error) {
	out, err := r.git(ctx, []string{
		"rev-list", "--end-of-options", base + ".." + tip,
	}, nil, capFor(int64(want)*(2*maxIDLen+1)), nil)
	if err != nil {
		return nil, err
	}
	bad := errors.New("intake: unexpected output of git rev-list")
	if len(out) == 0 || out[len(out)-1] != '\n' {
		return nil, bad
	}
	ids := strings.Split(string(out[:len(out)-1]), "\n")
	if len(ids) != want {
		return nil, bad
	}
	for _, id := range ids {
		if !gitenv.IsObjectID(id) {
			return nil, bad
		}
	}
	return ids, nil
}

// commitSizes returns the size of every commit object, from cat-file
// --batch-check, and refuses a size over maxCommitObject or a total over the
// budget before any object is read.
func (r *runner) commitSizes(ctx context.Context, ids []string) ([]int, error) {
	bad := errors.New("intake: unexpected output of git cat-file")
	out, err := r.git(ctx, []string{"cat-file", "--batch-check"}, strings.NewReader(strings.Join(ids, "\n")+"\n"),
		capFor(int64(len(ids))*256), nil)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, bad
	}
	sizes := make([]int, len(ids))
	total := 0
	for i, line := range lines {
		f := strings.Split(line, " ")
		if len(f) != 3 || f[0] != ids[i] || f[1] != "commit" {
			return nil, bad
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 {
			return nil, bad
		}
		if size > maxCommitObject {
			return nil, fmt.Errorf("%w: the commit %s is longer than the limit", ErrLimit, ids[i])
		}
		sizes[i] = size
		total += size
	}
	if total > len(ids)*commitBudget+maxCommitObject {
		return nil, fmt.Errorf("%w: the commits are longer than the limit", ErrLimit)
	}
	return sizes, nil
}

// commitObjects reads the objects of ids, whose sizes are known and bounded.
func (r *runner) commitObjects(ctx context.Context, ids []string, sizes []int) ([][]byte, error) {
	total := 0
	for i := range ids {
		total += sizes[i] + 2*maxIDLen + 16
	}
	out, err := r.git(ctx, []string{"cat-file", "--batch"}, strings.NewReader(strings.Join(ids, "\n")+"\n"),
		capFor(int64(total)), nil)
	if err != nil {
		return nil, err
	}
	bad := errors.New("intake: unexpected output of git cat-file")
	objects := make([][]byte, len(ids))
	for i, id := range ids {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 || string(out[:nl]) != fmt.Sprintf("%s commit %d", id, sizes[i]) {
			return nil, bad
		}
		out = out[nl+1:]
		if len(out) < sizes[i]+1 || out[sizes[i]] != '\n' {
			return nil, bad
		}
		objects[i] = out[:sizes[i]]
		out = out[sizes[i]+1:]
	}
	if len(out) != 0 {
		return nil, bad
	}
	return objects, nil
}

// commitErr returns an error of the commit id. The cause is the package's
// own text: it holds no lead text except what the caller escapes into
// detail.
func commitErr(kind error, id, detail string) error {
	return fmt.Errorf("%w: commit %s: %s", kind, id, detail)
}

// parseCommit reads one commit object.
//
// The headers are the lines up to the first empty line. A line that starts
// with a space continues the header before it. The first header must be
// "tree", then come the "parent" headers, then "author" and "committer";
// every header after them is free ("encoding", "gpgsig", "mergetag" and
// others), and only "encoding" is read. Each header, with its continuation
// lines, is at most maxCommitField bytes. The message is not kept, only its
// subject.
func parseCommit(id string, raw []byte) (Commit, error) {
	c := Commit{ID: id}
	const (
		wantTree = iota
		wantParents
		wantCommitter
		wantFree
	)
	state := wantTree
	sawEncoding := false
	rest := raw
	var name, value string
	haveHeader := false
	flush := func() error {
		if !haveHeader {
			return nil
		}
		haveHeader = false
		if len(value) > maxCommitField {
			return fmt.Errorf("%w: a header of the commit %s is longer than the limit", ErrLimit, id)
		}
		switch {
		case state == wantTree:
			if name != "tree" || !gitenv.IsObjectID(value) {
				return commitErr(ErrCommitFormat, id, "the first header is not a tree id")
			}
			state = wantParents
		case state == wantParents && name == "parent":
			if !gitenv.IsObjectID(value) {
				return commitErr(ErrCommitFormat, id, "a parent is not an object id")
			}
			c.Parents = append(c.Parents, value)
		case state == wantParents && name == "author":
			who, when, err := parseIdent(value)
			if err != nil {
				return commitErr(ErrCommitFormat, id, "the author header is not an identity")
			}
			c.AuthorName, c.AuthorEmail, c.AuthorTime = who.name, who.email, when
			state = wantCommitter
		case state == wantCommitter && name == "committer":
			who, when, err := parseIdent(value)
			if err != nil {
				return commitErr(ErrCommitFormat, id, "the committer header is not an identity")
			}
			c.CommitterName, c.CommitterEmail, c.CommitTime = who.name, who.email, when
			state = wantFree
		case state != wantFree:
			return commitErr(ErrCommitFormat, id, "the headers are not tree, parents, author, committer")
		case name == "encoding":
			if sawEncoding {
				return commitErr(ErrCommitFormat, id, "more than one encoding header")
			}
			sawEncoding = true
			if !strings.EqualFold(value, "UTF-8") && !strings.EqualFold(value, "UTF8") {
				shown, _ := leadtext.Line(value, 64)
				return commitErr(ErrCommitEncoding, id, `the encoding header is "`+shown+`"`)
			}
		}
		return nil
	}
	for {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			if len(rest) == 0 {
				// Headers with no empty line after them: an empty message.
				break
			}
			return Commit{}, commitErr(ErrCommitFormat, id, "a header line is not terminated")
		}
		line := rest[:nl]
		rest = rest[nl+1:]
		if len(line) == 0 {
			if err := flush(); err != nil {
				return Commit{}, err
			}
			c.Subject = subjectOf(rest)
			break
		}
		if line[0] == ' ' {
			if !haveHeader {
				return Commit{}, commitErr(ErrCommitFormat, id, "a continuation line has no header")
			}
			value += "\n" + string(line[1:])
			if len(value) > maxCommitField {
				return Commit{}, fmt.Errorf("%w: a header of the commit %s is longer than the limit", ErrLimit, id)
			}
			continue
		}
		if err := flush(); err != nil {
			return Commit{}, err
		}
		sp := bytes.IndexByte(line, ' ')
		if sp <= 0 {
			return Commit{}, commitErr(ErrCommitFormat, id, "a header has no name or no value")
		}
		name, value, haveHeader = string(line[:sp]), string(line[sp+1:]), true
		if len(value) > maxCommitField {
			return Commit{}, fmt.Errorf("%w: a header of the commit %s is longer than the limit", ErrLimit, id)
		}
	}
	if err := flush(); err != nil {
		return Commit{}, err
	}
	if state != wantFree {
		return Commit{}, commitErr(ErrCommitFormat, id, "the headers are not tree, parents, author, committer")
	}
	if len(c.Subject) > maxCommitField {
		return Commit{}, fmt.Errorf("%w: the subject of the commit %s is longer than the limit", ErrLimit, id)
	}
	return c, nil
}

// ident is the name and the address of an identity header.
type ident struct{ name, email string }

// parseIdent reads "Name <address> seconds zone" as git stores it. The name
// is the text before the first "<", without the white space at its end, and
// may be empty; the address is the text between that "<" and the next ">"
// and holds neither; then comes exactly one space, the seconds as decimal
// digits that fit an int64, one space and the zone as a sign and four
// digits. Nothing is converted.
func parseIdent(s string) (ident, time.Time, error) {
	bad := errors.New("not an identity")
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return ident{}, time.Time{}, bad
	}
	gt := strings.IndexByte(s[lt+1:], '>')
	if gt < 0 {
		return ident{}, time.Time{}, bad
	}
	gt += lt + 1
	email := s[lt+1 : gt]
	if strings.ContainsRune(email, '<') {
		return ident{}, time.Time{}, bad
	}
	tail := strings.Split(s[gt+1:], " ")
	if len(tail) != 3 || tail[0] != "" || tail[1] == "" || len(tail[2]) != 5 || (tail[2][0] != '+' && tail[2][0] != '-') {
		return ident{}, time.Time{}, bad
	}
	for _, part := range []string{tail[1], tail[2][1:]} {
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return ident{}, time.Time{}, bad
			}
		}
	}
	when, err := parseUnix(tail[1])
	if err != nil {
		return ident{}, time.Time{}, bad
	}
	return ident{name: strings.TrimRight(s[:lt], " \t\r\n"), email: email}, when, nil
}

// subjectOf returns what "git log --format=%s" prints for a message: after
// the empty lines at its start, the lines up to the next empty line, each
// without the white space at its end, joined by single spaces. A line is empty
// when it holds only white space (git's: space, tab, carriage return, newline). Git reads the message as a C string, so a
// NUL byte ends it.
func subjectOf(msg []byte) string {
	if i := bytes.IndexByte(msg, 0); i >= 0 {
		msg = msg[:i]
	}
	var parts []string
	for len(msg) > 0 {
		line := msg
		if nl := bytes.IndexByte(msg, '\n'); nl >= 0 {
			line, msg = msg[:nl], msg[nl+1:]
		} else {
			msg = nil
		}
		trimmed := bytes.TrimRight(line, " \t\r\n")
		if len(trimmed) == 0 {
			if len(parts) == 0 {
				continue
			}
			break
		}
		parts = append(parts, string(trimmed))
	}
	return strings.Join(parts, " ")
}

// parseUnix reads a decimal Unix time in seconds and returns it in UTC.
func parseUnix(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0).UTC(), nil
}
