- The daemon's seat gate no longer stops for good when accepting a connection
  fails, as it does when descriptors or memory run out: it tries again after
  a wait that starts at 5 ms and doubles to at most 1 s, so phone approvals
  come back by themselves once the cause is gone. The daemon logs
  `seat gate: accepting a connection failed; retrying until one succeeds` when
  such failures begin.
