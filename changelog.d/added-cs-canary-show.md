- Add two subcommands to `ataniq-canary`, the provisioning command of the
  canary slice, which exists only in a build with the `ataniq_canary` tag:
  `show --config CONFIG` prints, as `key=value` lines, the names, numbers and
  paths of the config that the attended run script needs (the role, the layout
  role, the unit, both roots, the holder's folders, the evidence file, the
  limits, the agent kind and arguments, the mirror, the lead's home and tree,
  the sandbox inputs path and the sources provisioning makes) and never the
  agent's environment, the git identity or the profile digest; `check-inputs
  --config CONFIG --inputs FILE` checks the sandbox inputs file against the
  config and names the field that differs, never its value. Neither changes
  anything on the host.
