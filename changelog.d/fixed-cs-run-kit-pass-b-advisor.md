- Fix the findings of the advisor review of the pass B run kit. Step 17 asks
  again (up to three times) on an empty answer instead of ending the pass after
  the login is spent, the dry run names the word `pasted`, and the pass B
  checklist now agrees with the run's prompts at steps 16, 17, 25 and 26 and
  with prepare's order (mirror qualification, then `netcheck.sh`). Step 25 takes
  only a credential file as proof that the canary wrote a login (not the state
  file), compares against the canary's own folder as it is at step 25 (also when
  step 15 left no manifest), and, when no login was completed, asks about a
  difference instead of counting it as a finding. The preflight of a live root says FIX
  when `agent.argv` has no `--model` (and prepare's note no longer says to
  prepare the same root again), and asks for `ss` only for a live root.
  `netcheck.sh` binds resolved's upstream resolv.conf only when the host's file
  is the local stub, as the holder does. AGENTS.md and CLAUDE.md name
  `netcheck.sh` as run only by the owner's hand. The README and the slice guide
  say that `show` prints the names of the agent's environment.
