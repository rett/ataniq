- The console's owner client asks the daemon again when the owner leaves the
  approvals view and opens it again, after an answer that the daemon has no
  owner channel, so a daemon restarted with `--owner-channel` is found without
  restarting the console. Between those actions it still remembers the answer
  and does not dial for each reading.
