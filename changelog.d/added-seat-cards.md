- `ataniq-seat` shows the cards on the phone, read-only, behind a session: the
  card list (`/cards/`), a card (`/cards/{id}`) and the pages of a card's
  whole path list (`/cards/{id}/paths/{n}`). A page without a session goes to
  the login page and asks the daemon nothing. The seat builds each card from
  the record's bytes with the card model after checking their digest, shows a
  lead's text only as escaped plain text, and says on every page that the
  desk runs a STAND-IN door on scratch state and that approve and deny are at
  the desk; no page has a decide control. Reads are limited to 20 a minute per
  paired credential, so that one phone and every login stay within the seat
  gate's 60 requests a minute.
