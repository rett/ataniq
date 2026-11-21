- Show a scratch lead `ready` once its holder reports up, in a build with the
  `ataniq_canary` tag under `ataniqd run --scratch`: after `lead.start`, and at
  daemon start for a lead shown `starting` that the daemon adopted, a background
  observer asks the holder's control socket for its status (for up to 60 s,
  retrying with a growing wait) and, when the holder answers for the same epoch
  and role, sets the shown state `ready` with the reason `holder-ready` and
  `ataniq-observation` evidence. `ready` means the holder is up and the agent
  was started; the agent's login is not observed, and it is not a claim that the
  agent works. `lead.start` still answers `starting` at once, a stop or a newer
  epoch wins over the observer, a slow or silent holder leaves the lead
  `starting`, and the daemon joins the observer at shutdown. The canary has no
  observer and is unchanged. See `docs/scratch-leads.md`.
