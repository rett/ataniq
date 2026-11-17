package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
)

// Git file modes, as numbers.
const (
	modeSymlink   = 0o120000
	modeSubmodule = 0o160000
)

// errPathTooLong is returned for a path over maxPathBytes.
var errPathTooLong = fmt.Errorf("%w: a path is longer than %d bytes", ErrLimit, maxPathBytes)

// rawRecord is one entry of the raw output of diff-tree.
type rawRecord struct {
	oldMode, newMode uint32
	oldID, newID     string
	status           byte
	path             string
}

// changes lists the net change of tip's tree against base's tree. Rename
// detection is off, so every path is literal.
func (r *runner) changes(ctx context.Context, base, tip string, lim Limits) ([]Change, error) {
	sink := newCapture(2*lim.MaxPaths, maxFieldBytes, "paths")
	_, err := r.git(ctx, []string{
		"diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-ext-diff", "--no-textconv", "--full-index",
		"--end-of-options", base, tip,
	}, nil, capFor(int64(lim.MaxPaths)*bytesPerRecord), sink)
	if err != nil {
		return nil, err
	}
	recs, err := parseRaw(sink.buf, false)
	if err != nil {
		return nil, err
	}
	if len(recs) > lim.MaxPaths {
		return nil, fmt.Errorf("%w: %d paths, the limit is %d", ErrLimit, len(recs), lim.MaxPaths)
	}
	changes := make([]Change, 0, len(recs))
	for _, rec := range recs {
		switch rec.status {
		case 'A', 'M', 'D', 'T':
		default:
			return nil, errors.New("intake: unexpected status in the output of git diff-tree")
		}
		if (rec.status == 'A') != (rec.oldMode == 0) || (rec.status == 'D') != (rec.newMode == 0) {
			return nil, errors.New("intake: unexpected modes in the output of git diff-tree")
		}
		changes = append(changes, Change{
			Path: rec.path, Status: rec.status,
			OldMode: rec.oldMode, NewMode: rec.newMode,
			OldID: rec.oldID, NewID: rec.newID,
		})
	}
	slices.SortFunc(changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	for i := 1; i < len(changes); i++ {
		if changes[i].Path == changes[i-1].Path {
			return nil, errors.New("intake: a path twice in the output of git diff-tree")
		}
	}
	return changes, nil
}

// parseRaw parses the NUL-separated raw output of diff-tree: for each entry
// one field ":<old mode> <new mode> <old id> <new id> <status>" and one with
// the path. With headers set, the output comes from diff-tree --stdin, and a
// field that is a full object id (the commit the entries belong to) may stand
// before the entries of a commit; it is skipped.
func parseRaw(out []byte, headers bool) ([]rawRecord, error) {
	bad := errors.New("intake: unexpected output of git diff-tree")
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, bad
	}
	fields := bytes.Split(out[:len(out)-1], []byte{0})
	var recs []rawRecord
	for i := 0; i < len(fields); {
		f := string(fields[i])
		if headers && gitenv.IsObjectID(f) {
			i++
			continue
		}
		if i+1 >= len(fields) || !strings.HasPrefix(f, ":") {
			return nil, bad
		}
		parts := strings.Split(f[1:], " ")
		if len(parts) != 5 || len(parts[4]) != 1 {
			return nil, bad
		}
		var rec rawRecord
		var err error
		if rec.oldMode, err = parseMode(parts[0]); err != nil {
			return nil, bad
		}
		if rec.newMode, err = parseMode(parts[1]); err != nil {
			return nil, bad
		}
		rec.oldID, rec.newID, rec.status = parts[2], parts[3], parts[4][0]
		if !gitenv.IsObjectID(rec.oldID) || !gitenv.IsObjectID(rec.newID) {
			return nil, bad
		}
		rec.path = string(fields[i+1])
		if rec.path == "" {
			return nil, bad
		}
		if len(rec.path) > maxPathBytes {
			return nil, errPathTooLong
		}
		recs = append(recs, rec)
		i += 2
	}
	return recs, nil
}

// parseMode reads a git mode: six octal digits.
func parseMode(s string) (uint32, error) {
	if len(s) != 6 {
		return 0, errors.New("mode")
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}
