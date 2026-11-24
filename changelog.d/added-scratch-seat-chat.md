- The daemon of `make scratch-seat-build` (both build tags) now gives the seat
  gate the scratch leads' replies: `build/scratch-seat/ataniqd run --scratch
  CONFIG --owner-channel --seat-gate IP:PORT` serves the capability `chat`, read
  only, from `leaddaemon.LeadChat`, and the gate's start line is followed by
  `seat gate: chat: replies the scratch leads wrote to their outboxes; read
  only`. The phone's Leads pages show what each lead wrote to its reply folder
  once the desk turned its chat on with `ataniqd chat set KEY read --config
  CONFIG`; every lead is off whenever the daemon starts, and a lead turned off
  is refused `chat-off` at once. `--trial` is refused with `--scratch` (exit
  status 2), so such a daemon never serves the phone trial's samples; the trial
  alone and the owner channel without `--scratch` are unchanged. The owner
  prepares the scratch roots, the non-trial `owner/scratch.json` of the state
  root, the gate's key and pinned seat key and the seat origin. See
  `docs/scratch-leads.md`, "Running real leads with the seat".
