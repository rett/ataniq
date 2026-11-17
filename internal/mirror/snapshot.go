package mirror

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
)

const (
	// maxSnapshotRefs is the most refs a snapshot of the whole mirror holds.
	// It is far above any listing cap: a mirror with more refs than this is
	// not one that a fetch works on.
	maxSnapshotRefs = 200_000
	// snapshotOutput is the output cap of the snapshot of the whole mirror.
	snapshotOutput = 64 << 20
	// snapshotRecord is the output bound for each ref of a snapshot that is
	// capped by a count. A ref name has at most 4096 bytes in git.
	snapshotRecord = 8192
)

// snapshot lists every ref of the repository at gitDir, raw, sorted by name,
// with symbolic refs and names that fail the layout rules. Two snapshots can
// be compared with [slices.Equal]. More than max refs, or more output than
// maxOut bytes, is an error ([ErrTooManyRefs] or the output limit of git's
// run). The names are lead text: nothing here prints them.
func (m *Mirror) snapshot(ctx context.Context, gitDir string, max int, maxOut int64) ([]record, error) {
	out, err := m.run(ctx, gitenv.Cmd{
		GitDir: gitDir,
		Args:   []string{"for-each-ref", listFormat, "--count=" + strconv.Itoa(max+1), "--end-of-options"},
	}, nil, maxOut)
	if err != nil {
		return nil, err
	}
	recs, err := parseRecords(out)
	if err != nil {
		return nil, err
	}
	if len(recs) > max {
		return nil, ErrTooManyRefs
	}
	slices.SortFunc(recs, func(a, b record) int { return strings.Compare(a.Name, b.Name) })
	return recs, nil
}

// snapshotMirror is the snapshot of all refs of the mirror.
func (m *Mirror) snapshotMirror(ctx context.Context) ([]record, error) {
	return m.snapshot(ctx, m.path, maxSnapshotRefs, snapshotOutput)
}
