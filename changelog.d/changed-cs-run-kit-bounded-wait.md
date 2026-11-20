- Bound the attended canary run's wait for the daemon. Each ask of the daemon's
  status is cut off after 5 seconds by `timeout(1)`, so a socket that accepts
  and never answers no longer holds each ask for about 10 seconds, and a wait
  ends after its 60 seconds plus one ask, about 65 seconds at most. Step 14 no
  longer says the ticket was taken when the ticket result names no file. The
  three variables that shorten the wait are read by the test entry only, and the
  preflight checks that `timeout` is on `PATH`.
