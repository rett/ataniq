- `ataniq-seat` shows the cards on the phone behind a session, read-only as
  the first step (the decide control of the Card page is described in the
  entry on approving and denying on the phone): the card list
  (`/cards/`), a card (`/cards/{id}`) and the pages of a card's whole path
  list (`/cards/{id}/paths/{n}`), each a GET (a `HEAD` is refused 405 and asks
  the daemon nothing). A page without a session goes to the login page and
  asks the daemon nothing. The seat builds each card from the record's bytes
  with the card model after checking their digest, shows a lead's text only as
  escaped plain text, shows a prune's ref line with the branch its deleted ref
  was merged into and that branch's tip, as the desk does, and says on every
  page that the desk runs a STAND-IN door on scratch state. The list and the
  pages of a card's path list have no decide control. Reads are limited to 20
  a minute per paired credential, and the reads of every session together to
  26 calls of the seat gate in 80 seconds (the first page of a card's path
  list may call it twice); a read past them says the desk is busy and asks
  nothing.
