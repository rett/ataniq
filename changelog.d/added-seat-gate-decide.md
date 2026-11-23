- The seat gate of `ataniqd run --owner-channel --seat-gate ADDR` serves a
  login and a decision from the owner's phone (the capabilities `login` and
  `decide`: `seat.login-challenge`, `seat.login`, `seat.challenge`,
  `seat.decide`) when the seat origin is configured: the owner-local file
  `ataniq/owner-local/seat-origin` in the configuration folder, holding
  `https://` and a lower-case domain name, read once at start. Each login and
  each decision, a deny included, is a passkey assertion with user
  verification over a challenge the daemon draws from its own record of the
  card, kept in memory for 5 minutes and one use by the seat that asked (at
  most 32 decision and 16 login challenges; a restart drops them), verified
  against a key on `owner/keys.log` that is not revoked and of a kind the
  verifier knows, and the seat origin. A login belongs to no project, so the
  scratch settings' `"kinds"` apply when a decision's challenge is asked and
  when the decision is made, not at a login. The key is checked
  again right before the decision is journaled, so a revoke at the desk that
  returned first wins. A phone decision is journaled as confirmed by passkey
  with the assertion's evidence, needs no activity at the desk, and goes
  through the day's limit as at the desk (an approve past it is held, a deny
  never). The highest sign counter is kept, and one that does not grow is
  reported as a possible copy, not refused. Without an origin, or with a file
  that is refused or holds no canonical origin, the gate serves pairing and
  the reads only, as before. The start line names the origin and what the gate
  serves, or why it serves pairing and the reads only. Scratch state and the
  STAND-IN door only; unqualified. See `docs/lead-daemon.md`.
