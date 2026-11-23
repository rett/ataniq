- `ataniq-seat` shows the cards on the phone behind a session, read-only as
  the first step (the decide control of the Card page is described in the
  other seat fragment, `added-seat-approve-deny.md`): the
  card list (`/cards/`), a card (`/cards/{id}`) and the pages of a card's
  whole path list (`/cards/{id}/paths/{n}`), each a GET (a `HEAD` is refused
  405 and asks the daemon nothing). A page without a session goes to
  the login page and asks the daemon nothing. The seat builds each card from
  the record's bytes with the card model after checking their digest, shows a
  lead's text only as escaped plain text, shows a prune's ref line with the
  branch its deleted ref was merged into and that branch's tip, as the desk
  does, and says on every page that the
  desk runs a STAND-IN door on scratch state. The list and the pages of a
  card's path list have no decide control. Reads are limited to 20 a minute per
  paired credential, so that one phone and every login stay within the seat
  gate's 60 requests a minute.
