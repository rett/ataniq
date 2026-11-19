package approvals

import (
	"strconv"
	"strings"

	"ataniq/internal/approval/card"
)

// verdictAdvice is the words that say what the verdict lines are.
const verdictAdvice = "advice of this program, not in any record"

// refusedLine is the one header line of a card that exists because a run
// refused. It holds no verdict detail.
const refusedLine = "this card is here because a verdict run refused"

// verdictLines returns the lines that show the verdicts of a card, in the
// host's words, to be drawn in the card's "after the decision" block. decided
// says that the owner decided the card. Before the decision nothing of a
// verdict is returned, except one line for a card that exists because a run
// refused. After it, the lines name both runs and say that they are advice.
func verdictLines(c card.Card, decided bool) []string {
	v := c.Verdicts
	if v == nil {
		return nil
	}
	if !decided {
		if v.Refused {
			return []string{refusedLine}
		}
		return nil
	}
	out := []string{"verdicts (" + verdictAdvice + ")"}
	if v.Refused {
		out = append(out, refusedLine)
	}
	for i, r := range v.Runs {
		out = append(out, runLine(i+1, r))
	}
	return out
}

// runLine is the line of one run.
func runLine(n int, r card.Run) string {
	say := "rejected"
	if r.Approve {
		say = "approved"
	}
	var checks []string
	for _, ck := range r.Checks {
		if !ck.Kind.Valid() {
			continue
		}
		res := "fail"
		if ck.Pass {
			res = "pass"
		}
		checks = append(checks, ck.Kind.String()+" "+res)
	}
	s := "run " + strconv.Itoa(n) + "  " + card.VerdictText(r.ID) + "  " +
		card.VerdictText(r.Provider) + " / " + card.VerdictText(r.Model) + "  " + say
	if len(checks) > 0 {
		s += "  (" + strings.Join(checks, ", ") + ")"
	}
	return s
}

// agreementLine returns the line that says how often the verdicts agreed
// with the owner's decisions in the listing, in the host's words, to be
// drawn in the list's header. It counts the recent decisions that hold
// verdicts: a decision agreed when both runs said what the owner decided. It
// returns nothing when none count.
func agreementLine(l card.Listing) string {
	agreed, total := 0, 0
	for _, it := range l.Recent {
		a, counted := it.Agreed()
		if !counted {
			continue
		}
		total++
		if a {
			agreed++
		}
	}
	if total == 0 {
		return ""
	}
	return "verdict runs agreed with you on " + strconv.Itoa(agreed) + " of " + strconv.Itoa(total) +
		" (" + verdictAdvice + ")"
}
