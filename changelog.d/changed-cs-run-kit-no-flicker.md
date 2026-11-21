- `canary-prepare.sh --pass B` now puts `CLAUDE_CODE_NO_FLICKER=1` in the live
  agent's environment beside `DISABLE_AUTOUPDATER=1`. It selects claude's
  fullscreen (alternate-screen) renderer, the only one the screen recogniser
  has fixtures for; without it claude chooses the renderer per process and the
  first pass B drew on the main screen, where the fence answered `not-now`.
  The preflight of a live root now requires both names in `agent.env` and gives
  a FIX naming the one that is missing. The environment is part of the profile
  digest, so a root prepared earlier needs a new root and a new pin. The kit's
  README, the checklist and `docs/canary-slice.md` say so, and
  `docs/lead-injection.md` says that every recorded claude screen is from the
  fullscreen renderer. Step 22 prints the two alternate-screen counters from
  `final.json`, and step 16 names `alt_screen_on` as the thing the lead reads;
  `alt_screen_on` above zero in the final record is what confirms the
  renderer choice, which is likely the cause and not yet confirmed by a run.
