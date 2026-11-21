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
- A scratch lead is shown `ready` once its holder reports up, in a build with
  the `ataniq_canary` tag under `ataniqd run --scratch`: after `lead.start`,
  and at daemon start for a lead shown `starting` that the daemon adopted, a
  background observer asks the holder's control socket for its status (for up
  to 60 s, retrying with a growing wait) and, when the holder answers for the
  same epoch and role, sets the shown state `ready` with the reason
  `holder-ready` and `ataniq-observation` evidence. `ready` means the holder
  is up and the agent was started; the agent's login is not observed, and it
  is not a claim that the agent works. `lead.start` answers `starting` at
  once. A proven stop or a newer epoch wins over the observer, a stop that is
  refused or fails leaves it, a slow or silent holder leaves the lead
  `starting`, and the daemon joins the observer at shutdown. The canary has no
  observer. See `docs/scratch-leads.md`.
- A scratch lead's `lead.list` rows carry two members, always present:
  `state_reason`, the reason code the registry shows with the shown state (a
  promoted lead reads `ready` with `holder-ready`), and `message_result`, the
  holder's three words (`outcome`, `reason`, `written`) about the current
  epoch's last message, empty when no message was sent or the answer is not
  known (the daemon keeps it while it runs and reads it from the command table
  after a restart while the message's receipt is the lead's last). The summary
  receipt of a row leaves out its `message` as well as its `result`, which
  keeps the largest list under 8 KiB. A row without the members is read by the
  console as one with both empty. See `docs/scratch-leads.md`.
- The epoch of a scratch lead takes one message, and a message that was not
  delivered does not spend it. When the holder answers `not-now` or `refused`
  with nothing written, or refuses the request itself (`disabled`,
  `wrong-epoch` or `bad-request`), the epoch's message stays free,
  `lead.list` shows `message_spent` as `false`, and the lead may send another
  message in the same epoch (the same message id is not refused). Everything
  that may have reached the lead stays spent: a submitted or uncertain result,
  no answer, a closed connection, an answer that does not decode, a refusal
  code the daemon does not know, a result or refusal that could not be written
  to the evidence, and a holder's `busy` (a holder that says a delivery is
  running is delivering something the daemon has no record of; the console
  shows the message as `sent-unsure`). A restarted daemon reads the evidence
  back and keeps the epoch free only when the message has its `inject-result`
  line with `not-now` or `refused` and nothing written, or its `inject-refused`
  line with `holder-disabled`, `holder-wrong-epoch` or `holder-bad-request`; a
  daemon killed after the `inject-sent`
  line and before the closing line leaves the epoch spent, as does any closing
  line without a matching `inject-sent` line or one that comes twice. The
  canary's rule that every sent message spends the epoch is unchanged. See
  `docs/scratch-leads.md`.
- The console's leads view says plainly what became of a message. A done
  message is drawn from the holder's three words (outcome, reason, written):
  "NOT delivered: nothing was written" with the reason (the lead's screen is
  not idle or is blocked, the program version is untested, the text was
  refused), "the message was submitted", or "result unknown" with what is
  known of what was written, and the line says whether the epoch's message is
  free (a message that was not delivered) or spent. The MSG column agrees:
  `free`, `free (not delivered)`, `sent` (only when the words say the message
  was submitted), `sent-unsure` (something may have reached the lead, the
  words are not known, or the daemon holds the epoch spent with none) or
  `spent, not delivered` (the daemon holds the epoch spent after a message that
  was not delivered because it could not write the evidence line), and `m`
  sends another message while the epoch's message is free. A lead whose state
  is `ready` with the reason `holder-ready` gets a line that says the holder is
  up and the agent started and that its login is not observed. See
  `docs/terminal-console.md`.
