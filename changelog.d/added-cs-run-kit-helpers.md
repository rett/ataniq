- Add two helper scripts to the canary run kit, in
  `qualification/canary/run/`. `canary-prepare.sh [--root DIR] [--dry]` writes
  the set-up of pass A under a new root (default `ataniq-canary-a` in the cache
  folder): the roots, a throwaway mirror, the config, the sandbox inputs, the
  replay screens and the stand-in copy, and it pins `holder.profile_digest`
  with `ataniq-canary profile-digest`. `canary-reset.sh --root DIR [--dry]`
  deletes only `ROOT/state` and `ROOT/runtime` of a prepared root after the
  owner types `reset` on a terminal, refuses while an `ataniq-*` unit is loaded
  or an `ataniqd` or `ataniq-holder` of the root runs, and makes the two roots
  and the mirror again. Both are run only by the owner's hand, and neither
  starts a unit, a daemon, a sandbox or an agent; `--dry` prints the plan and
  writes nothing.
- The run script now reads the facts of steps 1, 2, 3, 8, 12 and 20 itself,
  where the lead used to type `yes`, and fails the step when the fact is false.
  Step 1 fails on an open Qargi run of the checkout or a loaded `ataniq-*`
  unit, step 2 unless the two fact reports passed, step 3 unless HEAD is the
  commit of `develop` and no tracked file has a change, and steps 8, 12 and 20
  unless the mirror lists the upstream branch, `holder.json` says the sandbox
  started with the pinned profile version and digest, and the upstream branch
  is at the lead's commit. When it cannot read a fact (no `qargi`, a list it
  does not know) it asks the lead as before and says so in the log. The
  `--self-test` entry gains `open-runs`, `list-scan`, `loaded-units`,
  `tracked-changes`, `unit-facts`, `plan-facts`, `holder-start`, `upstream-id`
  and `lead-id`.
