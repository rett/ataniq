- Add canary mode to the lead holder, in a build with the `ataniq_canary` tag
  only: `ataniq-holder --canary --sandbox-inputs FILE` starts the agent through
  the start sequence of the lead sandbox with no controlling terminal, writes
  the waiver of assertion 10 into `holder.json` and the event log, and shows the
  OSC 7501 counters in its status; `--inject` opens injection through the
  control socket and `--expected-mode` names the footer mode a delivery needs.
  A start the sequence refuses exits with status 4 and leaves no agent and no
  `holder.json`. `ataniq-holder --version` prints the name and the version, and
  in a tagged build the tag. Without the tag the holder is unchanged apart from
  `--version`: the four flags are unknown and it hosts a scratch agent only.
