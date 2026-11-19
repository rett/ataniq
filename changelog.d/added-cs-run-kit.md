- Add the kit of the attended canary run. `make canary-build` runs doctor and
  builds, with the `ataniq_canary` tag, into `build/canary/` (ignored) the
  binaries the run uses (`ataniqd`, `ataniq-holder`, `ataniq-canary`,
  `ataniq-canary-standin`, `ataniq-console`, `ataniq-intake`, `ataniq-probe`;
  the stand-in and the probe static) and runs none of them; it is the one target
  that builds binaries with the tag, and no release and no CI job uses it.
  `qualification/canary/run/canary-run.sh --pass A|B --config CONFIG [--dry]
  [--evidence DIR]` is the run script, a POSIX shell script that starts a real
  unit, sandbox and agent and is run only by the owner's hand with the lead
  present. It prints each of the 26 steps with what it does, the expected
  result and the gate it serves, waits for Enter before every step that runs
  anything (the start, the read-back of the unit's limits and the balancing of
  the memory limit are one step with one key), never answers a prompt, and
  records each of the owner's own acts with the answer and the time. `--dry`
  prints everything, runs no command and needs no binary and no config;
  `--self-test NAME [VALUE...]` is the test entry, which gives sample values to
  one of the script's own functions, prints its answer and starts nothing. The
  evidence goes to a dated folder of mode 0700 with a manifest of names, sizes,
  times and hashes. A failure runs the clean-up chain: in pass B steps 22, 24,
  25 and 26 from step 6 on, and 21 and 23 as well from step 15 on; in pass A
  steps 22 and 26 from step 11 on. `qualification/canary/run/README.md` says
  what each pass needs beforehand, with a filled example of the config and of
  the sandbox inputs file, what the evidence folder holds, and the undo by
  hand. `tests/canary_runkit_test.go` checks the target, the `AGENTS.md` lines
  and the script, which it runs with `--dry` and `--self-test` only, over stubs
  that must not be called.
