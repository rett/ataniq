- `ataniq-seat` approves and denies on the phone, from the Card page only
  (`/cards/{id}`), when the seat gate offers decisions: one sign-off box per
  flagged path and Approve, offered when the card model allows it and the
  page showed every part of the card whole (a part with a path or a ref of
  more than 1024 characters, cut on the page, is not read; Approve is then
  withheld and the page says which part, and a page of the whole path list
  that cuts a path does not count) and enabled once every box is ticked; Deny, with an optional reason, available
  with no box ticked. A tap asks `POST /api/decide-challenge`; the seat
  builds the decision from the card it showed (the browser sends only the
  id of the page's form, the action, the indexes of the boxes and the
  reason), takes the tap only when that page showed the binding the session
  read last (a tab left open on a card replaced since is told the card
  changed, before any challenge or passkey), recomputes the daemon's
  challenge before the phone is asked, and shows the action, the card, its
  project, target and revision; a second tap asks one passkey assertion with
  user verification for the session's own credential, sent once by
  `POST /api/decide`. A cancel sends nothing. A lost answer shows "Decision
  sent. Outcome unknown." and only "Look again", and the card is not decided
  again until its Card page is read again by a GET whose read began after
  the decision was sent (an earlier read may show the card as it was, and
  then says to look again); nothing is queued. The pages of a card's whole
  path list count for the review they were read for, so pages read before
  a card was replaced do not count for the replacement. A held approve shows the
  host's line and the held words. Each refusal is fixed words; a passkey no
  longer paired ends the session. Decision challenges are limited to 10 a
  minute per credential, and an expired card gets none. A request body over
  8 KiB sent without a declared length is now refused 413, as one with it
  is. Scratch state and the STAND-IN door only; unqualified.
