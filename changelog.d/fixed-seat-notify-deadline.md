- The notifier's `notify` call has its own deadline of 15 seconds on both sides
  (`seatnotice` `NotifyDeadline`), where `info`, `subscribe` and `unsubscribe`
  keep 2: a push to the phone may take up to 5 seconds, with up to four
  subscriptions, and a call cut at 2 seconds was counted as failed after the
  push service had accepted the message, so the seat sent it again a minute
  later. The notifier bounds all the pushes of one notify to 12 seconds, and the
  seat's own bound on the call is 20 seconds, so the client's deadline ends a
  slow call.
