package intake

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/exchange/gitenv"
)

// commitFormat is the fixed record of one commit. Fields are separated by
// NUL, and with -z git ends each record with NUL too, so every commit is nine
// fields. "%an" and "%ae" are the stored bytes; the mailmap forms are not
// used.
const commitFormat = "--format=%H%x00%P%x00%an%x00%ae%x00%cn%x00%ce%x00%at%x00%ct%x00%s"

const commitFields = 9

// commits lists base..tip with git log. The commit count is checked first
// with rev-list --count, so a branch over the limit is refused before its
// text is read.
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
	sink := newCapture(n*commitFields, maxCommitField, "commits")
	// An empty --encoding keeps the message bytes as they are stored, with
	// no conversion to UTF-8.
	_, err = r.git(ctx, []string{
		"log", "-z", commitFormat, "--encoding=",
		"--no-mailmap", "--no-notes", "--no-decorate", "--no-show-signature",
		"--end-of-options", base + ".." + tip,
	}, nil, capFor(int64(n)*commitFields*maxCommitField), sink)
	if err != nil {
		return nil, err
	}
	return parseCommits(sink.buf, n)
}

// parseCommits parses the output of the log command above. It wants exactly
// want commits.
func parseCommits(out []byte, want int) ([]Commit, error) {
	bad := errors.New("intake: unexpected output of git log")
	if len(out) == 0 || out[len(out)-1] != 0 {
		return nil, bad
	}
	fields := strings.Split(string(out[:len(out)-1]), "\x00")
	if len(fields) != want*commitFields {
		return nil, bad
	}
	commits := make([]Commit, 0, want)
	for i := 0; i < len(fields); i += commitFields {
		f := fields[i : i+commitFields]
		if !gitenv.IsObjectID(f[0]) {
			return nil, bad
		}
		c := Commit{
			ID:             f[0],
			AuthorName:     f[2],
			AuthorEmail:    f[3],
			CommitterName:  f[4],
			CommitterEmail: f[5],
			Subject:        f[8],
		}
		if f[1] != "" {
			c.Parents = strings.Split(f[1], " ")
			for _, p := range c.Parents {
				if !gitenv.IsObjectID(p) {
					return nil, bad
				}
			}
		}
		var err error
		if c.AuthorTime, err = parseUnix(f[6]); err != nil {
			return nil, bad
		}
		if c.CommitTime, err = parseUnix(f[7]); err != nil {
			return nil, bad
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// parseUnix reads a decimal Unix time in seconds and returns it in UTC.
func parseUnix(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0).UTC(), nil
}
