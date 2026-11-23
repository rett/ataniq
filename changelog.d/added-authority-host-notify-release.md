- The authority host's release source has the notifier's part in
  `deploy/authority-host/notify/`, laid over the base tree and the seat's part:
  `ataniq-seat-notify.service` (user and group `seat-notify`, a group of its own that the seat's unit joins as a supplementary group, socket
  `/run/seat-notify/notify.sock` with mode 0660 in `RuntimeDirectory=`, key and
  subscriptions in `/var/lib/seat-notify`, `IPAddressDeny=any` and no allow in
  the file: the owner's drop-in allows the resolver and the push service's
  range, the seat's hardening, never enabled) and the validators and reload
  lists with its lines added (`test -L` of its link, a restart before the
  seat's). The seat's unit takes an optional word, `$SEAT_NOTIFY_ARGS`, from
  its environment file (empty by default), so `--notify-socket` is the
  owner's to set. `ah-release check` excuses this one unit, by name, from the
  stop list (it decides nothing) and does not count it as the decision service
  a release that is not static must have. The README ("The notifier") and
  DEPLOY.md ("Deploying the notifier") say what it installs, the owner's
  one-time steps in order, how to test it with `send-test`, what breaks when
  it is down (nothing but notices) and how to roll back; the README's seat
  section now says the seat dials the notifier's local socket, and both
  documents say that the front proxy's read timeout must exceed 30 seconds
  and its buffering be off for `/api/watch`, which the phone's pages hold up
  to 25 seconds.
