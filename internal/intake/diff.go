package intake

import (
	"context"
	"errors"
	"fmt"
	"io"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
)

// Diff writes the patch of the net change of the review to w, for the owner
// to read. It writes one header line of its own that names the base and the
// tip commit, and then the patch of git diff between the two commits, through
// leadtext.NewWriter, so no control byte of the lead reaches w except the
// newline and the tab. Everything after the first line is leadtext.Label:
// unverified lead data. The header can be imitated by a line of the patch, so
// a reader may trust it only as the first line.
//
// The patch has no rename detection and no colour, shows binary files as the
// one-line notice of git, and uses no external diff program and no text
// conversion. Git runs on the commit ids of r, never on ref names. More patch
// than lim.MaxDiffBytes stops the run with ErrLimit; what was written before
// stays in w, escaped. Zero fields of lim take their defaults.
func Diff(ctx context.Context, src Source, r *Review, w io.Writer, lim Limits) error {
	if r == nil || !gitenv.IsObjectID(r.Base) || !gitenv.IsObjectID(r.Tip) {
		return errors.New("intake: the review has no base and tip commit ids")
	}
	lim, err := lim.resolved()
	if err != nil {
		return err
	}
	if r.Merge {
		// A review made in merge mode is of a host-built merge, and the
		// repository's attribute file is checked before its patch is read.
		if err := checkAttributes(src); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "ataniq intake diff base=%s tip=%s\n", r.Base, r.Tip); err != nil {
		return err
	}
	if r.Base == r.Tip {
		return nil
	}
	lw := leadtext.NewWriter(w)
	_, err = newRunner(src, lim).git(ctx, []string{
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--full-index",
		"--end-of-options", r.Base, r.Tip,
	}, nil, lim.MaxDiffBytes, lw)
	if errors.Is(err, boundedexec.ErrPipeHeld) {
		// The writer may still be in use by the copy that was left behind.
		return err
	}
	// Close writes a partial rune as escaped bytes, also after a limit.
	if cerr := lw.Close(); err == nil {
		err = cerr
	}
	return err
}
