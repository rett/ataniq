- The seat offers the guided new-project interview on the phone when the
  daemon's gate lists `genesis`: a "New project" button on the card list and
  the projects in progress, then pages of one to three multiple-choice
  questions of the desk's own catalog (each option with a one-line example,
  "Other" in your own words, a short note of about 200 letters (200 bytes), "You decide for me" and
  "Skip" where the question allows them), a summary grouped by what you chose,
  what was decided for you and the defaults, a "Change" on each row, "Looks
  right: finish" and "Discard this project". The pages are made on the server
  and each act is a plain form answered with a redirect; a refusal is a fixed
  line, and text that looks like a password or a key is refused and never
  shown back. Nothing starts and no agent is given the answers: they are kept
  by the daemon in `owner/genesis/` of its state folder and sent nowhere else.
  `seat.genesis` has a new verb, `list`, which answers the open projects, so a
  restarted seat finds them again. The seat keeps 12 of the gate's 60 calls a
  minute (in its 80-second window) for the interview, taken from the reads (now
  14) and the decisions (now 10, 5 decisions). `ataniq-seat` needs nothing
  configured for it, and logs the capabilities each hello of the gate lists,
  with whether the phone offers "New project". Every response of the seat now
  carries `Referrer-Policy: same-origin` (it was `no-referrer`), under which a
  browser keeps the `Origin` header of a form's post to the seat and still
  sends no address to another site; a proxy in front must not set a policy of
  its own on the seat's site, or the project pages' forms are refused.
