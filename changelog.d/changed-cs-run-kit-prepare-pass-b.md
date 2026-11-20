- Prepare pass B of the attended canary run the way pass A is prepared:
  `canary-prepare.sh --pass B --claude PATH [--model NAME]` copies the tested
  claude (one regular file, never a link) to `ROOT/agent-install/claude` after
  its `--version`, run with an empty environment, names 2.1.325, and writes a
  live config (`agent.argv` the executable itself, `agent.env`
  `DISABLE_AUTOUPDATER=1`, `footer_mode` `manual`), the inputs file with the
  terminal values, and a pin computed last, after the environment is in place.
  It also writes `netcheck.sh`, which the owner runs by hand to check DNS,
  HTTPS and `claude --version` in the sandbox's shape and under the unit's
  limits, and a pass B `CHECKLIST.txt`. The preflight checks a pass B root's
  claude copy, version, `DISABLE_AUTOUPDATER` and footer mode, and
  `ataniq-canary show` prints the names of `agent.env`, never a value. The
  README gains "The set-up of pass B".
