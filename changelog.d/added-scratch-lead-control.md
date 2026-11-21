- Add the lead control plane for scratch leads, in a build with the
  `ataniq_canary` tag only: `ataniqd run --scratch CONFIG` serves six
  operations (`lead.list`, `lead.define`, `lead.start`, `lead.stop`,
  `lead.ticket` and `lead.inject`) through the client protocol's extension
  seam, for up to eight leads of throwaway `scratch-` projects named in an
  owner-only config, with a `max_running` cap. Each command is bound to a client
  string and a command id in a durable table (`lead-commands.jsonl`) and answered
  with a receipt (`in-progress`, `done`, `refused`, `unavailable` or
  `uncertain`); a replay returns the stored receipt and never runs the command
  again, and a daemon restart ends an unfinished command as `uncertain` (a
  ticket as `unavailable`). The
  place gate for a scratch root is the marker `scratch-root.json`, written only
  by the new `ataniq-canary scratch-init`; `ataniq-canary scratch-clean` removes
  what a define left when it was cut short, by the owner's hand. The daemon
  computes each lead's sandbox profile digest with the holder's own code unless
  the owner pins one, which weakens the canary's independent pin; the client
  string is a namespace, not an identity. `ataniq-console --daemon` offers a
  leads view on `L` when the daemon lists the six operations (define, start,
  stop, message and attach, with the receipt of each command, then back to the
  list) and says "no lead control in this daemon" otherwise. A release daemon
  contains none of it and registers no operation; the release console carries
  the inert leads view. See `docs/scratch-leads.md`.
