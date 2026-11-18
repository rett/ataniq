// Package pager is the console's read-only pager for a patch.
//
// The patch is text a lead wrote, so it is untrusted. The pager reads it
// through a Source, page by page, and is the one place that makes it safe to
// draw: every line goes through leadtext.Escape once, after the pieces of a
// line that arrived in several pieces are joined and before the line is
// wrapped. What reaches the cells is escaped text only. The pager's own words
// (the header, the footer and the mark on a wrapped row) stand in rows and
// columns of their own and never hold text of the lead.
//
// A Model is a part the cockpit places: the parent hands it the messages it
// receives (Update), tells it the size of its region (SetSize) and has it
// draw into that region of a cell buffer (Draw). The pager never ends the
// program. It reports Esc to the parent as Close and handles no key but its
// scroll keys.
//
// The pager calls the Source only from a command (tea.Cmd), never from
// Update, SetSize or Draw, so a slow source cannot hold the keys. The first
// command it returns, from Init or from SetSize, reads the first page; the
// parent runs every command it is given and hands the answer to Update.
//
// The pager does not hold the patch. It keeps the pages that carry the rows
// it looks at (one screen above the top row and two from it on) and one
// page on either side, lets the others go, and asks again for a page it let
// go when the reader scrolls back. No wrapped row is kept: rows are wrapped
// when they are drawn, and for a line of many rows the pager remembers
// where every 32nd row begins. A line is held whole while it is near the
// screen, since it is joined before it is wrapped; MaxLineBytes bounds it.
//
// A long line is wrapped onto as many rows as it needs, each after the
// first with a mark in the left margin, and is never cut. The footer says
// how many pages lie below and whether the end was reached.
//
// A page that fails, a page that does not fit the others, or a line longer
// than MaxLineBytes ends the reading: the footer shows one line of the
// pager's own words, no further page is asked for, and Reached stays false.
// Reached is true only once the last piece of the last page was read and its
// last row was on the screen.
//
// The package starts no program and opens no file. Of this repository it
// imports only leadtext.
package pager
