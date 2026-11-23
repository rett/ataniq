- `ataniq-seat-notify` (command `cmd/ataniq-seat-notify`, static, the standard
  library and packages `webpush` and `seatnotice` only) is the notifier of the
  seat's notices: the one unit that holds the VAPID key and the push
  subscriptions and talks to a push service. `vapid-init --key PATH` makes a
  new key file of mode 0600, never overwriting, and prints the public key
  only. `run` serves the seatnotice protocol on a Unix socket (mode 0660) to
  one user, the seat's, looked up once at the start and checked by the
  kernel's peer credentials; it keeps at most 4 subscriptions in a strict,
  atomically written file of mode 0600, accepts only endpoints on the hosts
  of `--push-hosts` (exactly `web.push.apple.com` by default, https and port
  443), sends `{"n":N}` with the topic `waiting` to each in turn, drops a
  subscription the push service says is gone, and sends at most 30 pushes in
  any hour. It logs fixed text and counts, never an endpoint or a key.
  `send-test --socket PATH N` is the owner's one test push, run as the seat
  user.
