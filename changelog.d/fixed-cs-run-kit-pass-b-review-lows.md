- Fix the findings of the review of the run script's pass B fixes. Step 16's
  question also takes `go-on` (the fence then answers not-now and the owner
  pastes the message), and any other answer ends the pass with the reason.
  Step 15 asks again until `logged-in` or `failed`. Step 25 counts a changed
  login file as a finding only when its new hash is one the canary's own config
  folder holds; any other change becomes a question to the owner and the lead,
  and a new `projects/-work-*` path (the sandbox's working folder) is always a
  finding. Step 20's display of the token file opens the file once, checks the
  opened file, is bounded and timed, and never follows a symlink or waits on a
  FIFO. Step 23 prints the login time in local time too, as about. Step 26 looks
  for `code=` only in the form of a URL query.
