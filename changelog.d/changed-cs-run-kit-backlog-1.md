- Fix the attended canary run kit's texts after passes A and B. Step 5 of pass B
  now asks for `confirmed` only when the account used for the sandbox login
  lists that login as its own revocable entry, or a separate throwaway account is
  used (pass B run 1 showed Claude Code logins listed as Unknown rows), and step
  23 says how to identify the login and accepts `cannot-identify: <what you see>`
  as a finding. Step 20 prints the review's `Digest:` and `Tip:` lines and asks
  `integrate succeeded` up to three times. The clean-up chain no longer
  overwrites the first proof of the stop between the epochs (the files it writes that
  already exist get the suffix `-chain`). The preflight and the checklists list the daemon at step 10
  and say why. `docs/canary-slice.md` records what pass B showed.
