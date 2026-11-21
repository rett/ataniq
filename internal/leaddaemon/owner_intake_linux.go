//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"errors"
	"fmt"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/exchange/leadtext"
)

// UnescapingIntake wraps the intake the service reads the mirror through and
// turns the lines of a patch back into raw bytes before the service pages
// them. The intake's diff writes its patch through leadtext.NewWriter, whose
// stream form passes the tab as it is and escapes every other control byte
// (and the backslash), so a line holds a raw tab but no other byte below 0x20.
// The service wants raw lines (the pager of the console is the one place that
// escapes, and a page splits a long line into pieces that are rejoined byte
// for byte), so the daemon reverses the escaping, line by line, with
// [leadtext.Unescape], which leaves a raw tab as it is. Everything else is the
// inner intake's.
//
// A line that is not what the writer writes is not passed on: a byte below
// 0x20 other than the tab, or 0x7f, means the line did not come through the
// writer, and a backslash sequence the escaping does not write fails to
// unescape. The read of the patch then fails as a whole and the owner sees no
// patch, never a patch that was only partly turned back.
type UnescapingIntake struct {
	ownerchannel.Intake
}

// ErrPatchNotEscaped says a line of the intake's patch is not in the form the
// escaping writes.
var ErrPatchNotEscaped = errors.New("leaddaemon: a line of the patch is not in the escaped form")

// Patch reads the patch from the inner intake and unescapes its lines.
func (u UnescapingIntake) Patch(ctx context.Context, ref ownerchannel.ReviewRef) (ownerchannel.ReviewPatch, error) {
	rp, err := u.Intake.Patch(ctx, ref)
	if err != nil {
		return ownerchannel.ReviewPatch{}, err
	}
	raw := make([]string, len(rp.Lines))
	for i, line := range rp.Lines {
		for j := 0; j < len(line); j++ {
			// The tab is the one control byte the intake's writer passes raw.
			if c := line[j]; (c < 0x20 && c != '\t') || c == 0x7f {
				return ownerchannel.ReviewPatch{}, fmt.Errorf("%w (line %d)", ErrPatchNotEscaped, i)
			}
		}
		s, err := leadtext.Unescape(line)
		if err != nil {
			return ownerchannel.ReviewPatch{}, fmt.Errorf("%w (line %d)", ErrPatchNotEscaped, i)
		}
		raw[i] = s
	}
	rp.Lines = raw
	return rp, nil
}

var _ ownerchannel.Intake = UnescapingIntake{}
