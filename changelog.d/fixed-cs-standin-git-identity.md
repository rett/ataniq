- The canary stand-in agent now gives its git the `GIT_CONFIG_GLOBAL` of its own
  environment (a new `GitConfigGlobal` field of `standin.Config`, filled by
  `ataniq-canary-standin` from the environment; it is the only variable besides
  `HOME` that git gets). The sandbox names the identity file that provisioning
  wrote with that variable, and the stand-in had dropped it, so inside the
  sandbox its commit failed with `unable to auto-detect email address` (the
  second epoch's `result.txt` said `failed: git commit`) and the pass could not
  show a commit. New tests lay the world out as the sandbox does: a repository
  with no identity of its own, an empty home, and the identity only in the file
  the variable names. This is in a build with the `ataniq_canary` tag only.
