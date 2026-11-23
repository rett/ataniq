- The seat gate guide and design now describe the phone's notices. A notice is
  a push to the Home Screen app that says "N waiting", sent by the notifier
  (`ataniq-seat-notify`) when the count of waiting cards rises above what was
  last told, with no page of the seat polling in the last 60 seconds, at most
  once in 2 minutes when the count does not rise, at most 30 a day, and never
  while the seat is not live; it shows nothing of a card, has no action and a
  tap opens the card list. The guide ("Notices" in docs/seat-gate.md) gives the
  steps to turn notices on at the phone (Add to Home Screen, open it from there,
  Enable notices, allow), to stop them and to test with `send-test`, the status
  texts, and troubleshooting rows; the design ("Notices" in
  docs/design/seat-gate.md) gives the protocol bounds, the service worker's
  rules, the content security policy's `manifest-src` and `worker-src`, the
  Trusted Types policy `seat-sw` for the service worker's registration, the
  notifier's egress and limits, the privacy cost (Apple's push service sees
  the timing and size of each push) and what a taken-over seat or notifier can
  do. Both say that the seat reaches no network address but the gate's, and, for
  notices, the notifier's local socket. Notices are built, not deployed and not
  tried on a phone.
