- `ataniq-seat` approves and denies on the phone, from the Card page only
  (`/cards/{id}`), when the seat gate offers decisions (on a gate that does
  not, the Card page holds no form and allows no passkey): one sign-off box
  per flagged path and Approve, offered when the card model allows it and the
  page showed every part of the card whole (a part with a path or a ref of
  more than 1024 characters, a name in the header of more than 64, or a line
  of the plan or of the lead's note of more than 4000, cut on the page, is
  not read; Approve is then withheld and the page says which part, approve
  at the desk, and a page of the whole path list that cuts a path does not
  count) and enabled once every box is ticked; Deny,
  with an optional reason, available with no box ticked. A tap asks `POST
  /api/decide-challenge`; the seat builds the decision from the card it showed
  (the browser sends only the id of the page's form, the action, the indexes
  of the boxes and the reason), takes the tap only when that page showed the
  binding the session read last (a tab left open on a card replaced since is
  told the card changed, before any challenge or passkey), recomputes the
  daemon's challenge before the phone is asked, and shows the action, the
  card, its project, target and revision; a second tap asks one passkey
  assertion with user verification for the session's own credential, sent once
  by `POST /api/decide`. A cancel sends nothing. A lost answer shows "Decision
  sent. Outcome unknown." and only "Look again", and the card is not decided
  again until its Card page is read again by a GET whose read began after the
  answer was lost (a read made while the decision was on its way, or begun
  before, may show the card as it was, and then says to look again); nothing
  is queued. A page counts as read, and a Card page settles a lost answer,
  only once it was written to the phone. The pages of a card's whole path
  list count for the review they were read for, so pages read before a card
  was replaced do not count for the replacement. A held approve shows the
  host's line and the held words. Each refusal is fixed words; a passkey no
  longer paired ends the session. Decision challenges are limited to 10 a
  minute per credential, and an expired card gets none. A decision's words go
  to the seat gate exactly as the seat checked them against the gate's cap (a
  flagged path full of `&`, `<` or `>` is no longer escaped on the way, so it
  does not grow past the cap); a decision whose words are over the cap is not
  sent and the phone says "This is too large to send."; it is decided at the
  desk. So is one whose decision record the daemon could not journal (its
  sign-offs past the record's 8 KiB): the daemon refuses its challenge
  before the phone is asked, and nothing was decided. A request body over 8 KiB sent without a declared length is now
  refused 413, as one with it is.
  Scratch state and the STAND-IN door only; unqualified.
