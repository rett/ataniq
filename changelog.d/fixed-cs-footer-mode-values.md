- Refuse, at `ataniq-canary show` and `check-inputs`, a live canary agent with
  `inject` true whose `agent.footer_mode` is not `manual` or `auto`, the
  screen recogniser's names of the mode, so that the footer's text (`manual
  mode on`, or the old sample `sample mode on`) is caught before the message
  is spent instead of ending every inject `not-now`. A live agent's
  `agent.argv[0]` must also be a file under `/ataniq/agent/` that exists as a
  regular file in `sources.agent_install` (no wrapper), and step 9 refuses one
  that is not under it. The README and the canary slice document the values,
  that the install folder is `sources.agent_install` of the inputs file, and
  the order of the inputs file and the pin. This is in a build with the
  `ataniq_canary` tag only.
