// Package approvals is the console's one view of approvals: the list of
// requests and the card of one request, as a component the cockpit places.
//
// The parent (the cockpit) makes a Model with New, hands it every message it
// receives while the view is open (Update), tells it the size of its region
// (SetSize) and has it draw into that region of a cell buffer (Draw). The
// view never ends the program: it reports what the parent is to do in the
// Action that Update returns (close the view, quit, open the limit's screen
// for a project, open the lead).
//
// # The backend
//
// Everything the view knows about requests comes from a [Backend], which
// this package declares and whose signatures use only types of the card,
// record and limits packages and the standard library. The view calls the
// backend only from commands (tea.Cmd), never from Update, SetSize or Draw,
// so a slow backend cannot hold the keys. An error from the backend is shown
// as one line in the view's own words, with the error's code when it is a
// *record.Error, and never with the error's text.
//
// # The list
//
// The list shows the pending cards (a held one marked "decided past the
// limit"), the recent decisions, uses and refusals, and the standing rules,
// which are read-only. Its header carries the count from the first card, and
// the label for samples when the listing says its cards are samples. j and
// k or the arrows move, Enter opens a card, L reports the project of the row
// under the cursor to the parent (this package draws no screen for the
// limit), and Esc closes the view.
//
// # The card
//
// The card is drawn from a [card.Card] in the layout of the design: the
// title with the confirm code, the verb line, the plan block, the ref lines,
// the diff summary, the four lists each shown even when empty, the flagged
// paths each with a sign-off box, the evidence, the lead's note with its
// label and the keys. Long lists scroll, and the footer says how much lies
// below. A path list that the record holds only in part is read page by page
// through Backend.Page, and every page is reported to the card's [card.Reading]
// as it is shown; the view never jumps over a page for the reader. d opens
// the pager on the full patch.
//
// # Approve and deny
//
// Space signs off the flagged path under the cursor. a opens the confirm
// field only when card.Card.MayApprove says yes, and otherwise shows its
// reason in one line. The confirm code has four characters, is made again
// for every opening of a card from a reader that the parent injects, is
// drawn only in the card's title and is kept only in this model: it is in no
// value handed out. The field refuses, and clears, on bracketed paste, on a
// burst of key presses (two less than 30 ms apart, by the injected clock),
// on any input in the first 400 ms after the card opened, and on anything
// that is not a single printable key press, which covers what a terminal
// answers by itself. A wrong code clears the field and makes a new code. On
// Enter with the right code the view hands the card's Decision to
// Backend.Decide once, shows "sending", and then shows the outcome line. x
// denies, with an optional reason of at most 500 bytes.
//
// While a field is open every printable key goes to the field; only Enter,
// Esc and ctrl+c act, and ctrl+c is reported to the parent.
//
// # Hostile text
//
// Every string that a lead wrote reaches the view as a [card.LeadText] and
// is drawn only through its escaping, in rows of its own, near the label
// "unverified lead data". Every string is also drawn one character at a
// time, so no byte that a terminal would act on reaches a cell, whoever
// wrote the string. The view's own words (the title, the hints, the footer,
// the labels and the plan block's host wording) never hold text of a lead;
// the one exception is the feature name, which passed the project's grammar
// and is drawn as "name=...", so that it cannot stand for anything but a
// name.
//
// Of this repository the package imports only card, record, limits, leadtext
// and pager. It starts no program, opens no file and no socket.
package approvals
