- The seat gate's live wait can carry a chat hint, opt-in on both sides: a gate
  that serves `watch` and `chat` and whose chat port gives a cursor lists the
  new capability `wait-chat`, last, and then also takes `seat.wait` params
  with `chat` (the cursor the seat last saw, 0 for none) and answers with
  `chat`, the cursor now, an integer up to 2^53-1 and never any text. The
  cursor is a keyed digest (HMAC-SHA-256 under a random key the daemon makes
  for each binding of its source and never sends) over the leads whose chat
  the desk switched on (their key, switch generation and head) and nothing
  else: a lead whose chat is off moves no cursor and changes no answer or its
  timing, and a cursor says nothing a seat can recognise across a restart.
  Cursor equality is only a change hint and never authorizes a read. A chat
  change answers a held wait no sooner than 18 seconds after it began, a Card
  change at once, and a cursor source that fails gives the plain Card result
  with no `chat`. The extended wait never recovers an uncertain journal: it
  answers `unavailable` and leaves the recovery to the daemon's tick. A seat
  or gate that does not know the extension sends and receives exactly the old
  shapes; the plain form now refuses a `chat` member. The phone trial's gate
  lists it (its start line ends `... genesis, chat and wait-chat`); its
  STAND-IN cursor never moves within a run.
