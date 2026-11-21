// Package leadctl is the console's view of lead control: a table of the
// leads of a daemon, with the keys that define, start, stop and message a
// lead and that ask for an attach ticket, as a component the cockpit places.
//
// The parent (the cockpit) makes a Model with New, hands it every message it
// receives while the view is open (Update), tells it the size of its region
// (SetSize) and has it draw into that region of a cell buffer (Draw). The
// view never ends the program: it reports what the parent is to do in the
// Action that Update returns (close the view, quit, attach to a lead).
//
// # The backend
//
// Everything the view knows of the leads comes from a [Backend], which this
// package declares and whose signatures use only types of this package and
// the standard library, so that the view imports no client. The view calls
// the backend only from commands (tea.Cmd), never from Update, SetSize or
// Draw, so a slow backend cannot hold the keys. A command that changes
// something calls the backend once.
//
// # The keys
//
// j and k or the arrows move over the rows. d defines the lead under the
// cursor and s starts it. x asks first, "Stop <key> (epoch N)?", and only y
// stops it: any other key keeps it running and sends nothing. m opens a
// one-line editor for a message to the lead, for the epoch the row shows;
// Enter sends it once, Esc drops it, and every printable key, q and j
// included, is text. w and v ask for an attach ticket as a writer or as a
// viewer, and when the receipt is done the view asks the parent to attach
// (Action.Attach: the folder and ticket file the receipt carries). r reads
// the list again, Esc closes the view and ctrl+c quits, dropping an open
// field first.
//
// While a command for a lead has no answer yet the keys that would send
// another for it do nothing and say so. A message whose answer was lost, or
// whose receipt is not final, is not made again for the epoch: a second
// message would be a new action for an outcome that is not known, and the
// list says whether the epoch's message was sent.
//
// # Receipts
//
// Every command is shown with its receipt: accepted (in progress or done),
// refused, unavailable or uncertain. The view draws only words of its own
// for them, chosen by the receipt's status, operation, outcome and reason
// code. A code it does not know is drawn as a line that says so, and the text
// of an error is never drawn: a lost answer is one fixed line, and the list
// is read again. A done message is drawn from the holder's three words (the
// receipt's Outcome, Reason and Written): whether the message was submitted,
// NOT delivered (nothing was written) or written with an unknown result, and
// that the epoch's one message is spent, as the daemon makes it. A row whose last receipt is in progress is read again every
// two seconds until it is final.
//
// A receipt with Earlier set answers a command that an earlier press sent and
// kept, and is drawn as "an earlier <operation> command": it never makes an
// attach, and it does not settle the epoch's message. The list's LeftOut, and
// the rows the view drops itself, are shown as one fixed line.
//
// # Peer text
//
// The names of leads, states and units come from the daemon. Each is cut and
// drawn through a short ASCII subset, any other byte becoming a question
// mark, and every string is drawn one character at a time, so no byte that a
// terminal would act on reaches a cell. The reason codes and outcomes of
// receipts are looked up in tables of fixed texts.
//
// This package imports nothing of this repository. It starts no program,
// opens no file and no socket.
package leadctl
