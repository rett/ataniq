// Package arrival holds three small parts the cockpit places for the owner's
// approvals: the arrival summary, the status element and the bell.
//
// The arrival summary is a [Strip]: a few rows that tell the owner, at the
// moment he comes back to the console, what happened while he was away.
// Alerts and stops come first, then the cards that wait, then what was
// approved without him, then the notices that were held. It is shown once
// per arrival and is never a window that blocks.
//
// The status element ([StatusElement]) is the short text of the status line,
// "approvals 2 (oldest 3 min)", with a mark that says when to draw it in the
// alert style.
//
// The bell ([Bell]) says whether to ring for a card that has just arrived,
// and gives the command that rings.
//
// # What the package is
//
// Each part is fed plain values by the cockpit: counts, ages, times and lines
// the host wrote. The package asks no backend, reads no clock, starts no
// program and opens no file. Of this repository it imports only presence,
// whose arrival event drives the strip.
//
// # Text
//
// No part is meant to hold text a lead wrote. The strip still treats every
// line it is given as if it were such text: it keeps a short beginning of
// it, and where it is drawn each character that a terminal would act on or
// hide is replaced by a question mark. Nothing but printable characters of
// one or two cells reaches a cell.
//
// # Keys
//
// The strip takes no key that was aimed elsewhere. It never takes the key
// that woke the console, it takes a key only while the cockpit says the
// strip has the focus, and then only Esc and the digits that open one of its
// lines. Opening a line decides nothing: a card is decided only on the card,
// with its own key; an approval also needs a typed code, a denial needs none.
package arrival
