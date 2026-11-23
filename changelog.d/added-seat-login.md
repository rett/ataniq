- `ataniq-seat` has a login page, `/login/`, for a phone with a passkey paired
  at the desk. The seat asks the daemon through the seat gate for a login
  challenge and its allow-list, recomputes the challenge from the gate's pin
  before the phone is asked, and keeps it with the browser that asked; the
  phone signs it with user verification, and the daemon verifies the
  assertion. A login starts a session: a `__Host-seat` cookie (Secure,
  HttpOnly, SameSite=Strict) holding a fresh 256-bit value kept server-side,
  ended after 30 minutes idle, 12 hours at most, or by `POST /api/logout`; the
  Log out button goes to the login page only when the seat answered the
  logout, and otherwise says "Logout not confirmed" and stays. Login
  challenges are limited to 10 per client in 15 minutes and 8 a minute in all.
  The seat counts every call it sends the gate, by kind, over 80 seconds, and
  keeps each kind to its room there: pings 6, pairings and logins 12 (6
  logins), reads 26, decisions 16 (8 decisions), which make the gate's 60 a
  minute; a call without room is not sent and the phone says the desk is busy.
  A decision's challenge is asked only with room for the decision too, kept
  for that one challenge, so a decision the phone signed is never refused for
  the gate's rate, from any session of the passkey.
  A login belongs to no project and to no card: it proves that the phone holds
  a paired key of a kind the daemon knows, and decides nothing. The login
  page, and the Card page when it offers a decision, alone allow
  `publickey-credentials-get`, to the seat's own origin. A gate that has the
  seat origin serves the login; a gate without one answers that the desk does
  not take phone logins.
