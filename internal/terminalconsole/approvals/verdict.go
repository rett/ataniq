package approvals

import "ataniq/internal/approval/card"

// verdictLines returns the lines that show the verdicts of a card, in the
// host's words, to be drawn in the card's "after the decision" block. decided
// says that the owner decided the card; a verdict is never shown before. It
// returns nothing yet: the display of verdicts fills it.
func verdictLines(c card.Card, decided bool) []string {
	return nil
}

// agreementLine returns the line that says how often the verdicts agreed
// with the owner's decisions in the listing, in the host's words, to be drawn
// in the list's header. It returns nothing yet: the display of verdicts fills
// it.
func agreementLine(l card.Listing) string {
	return ""
}
