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
  free (a message that was not delivered, as far as the list's row says) or
  spent; the line of a message that was not delivered is not drawn "free" while
  the daemon holds the epoch spent, and says so until the list is read. The MSG
  column agrees:
  `free`, `free (not delivered)`, `sent` (only when the words say the message
  was submitted), `sent-unsure` (something may have reached the lead, the
  words are not known, or the daemon holds the epoch spent with none) or
  `spent, not delivered` (the daemon holds the epoch spent after a message that
  was not delivered because it could not write the evidence line), and `m`
  sends another message while the epoch's message is free. A lead whose state
  is `ready` with the reason `holder-ready` gets a line that says the holder is
  up and the agent started and that its login is not observed. See
  `docs/terminal-console.md`.
- The console's leads view keeps what became of a message on its status line
  when the list is read again. The daemon's list holds each lead's last command
  as a summary without its result, and the view keeps the answer's three words
  (outcome, reason, written) of the message just sent, and a ticket's folder and
  ticket file, while the list shows the same command; a list receipt that has
  words of its own is shown as it is. See `docs/terminal-console.md`.
- The console's leads view reads the list again every two seconds while a lead
  is `starting` or `stopping`, as it already did while a receipt is in
  progress, so a started lead turns `ready` without `r` and the reading stops
  when none is. A lead that is `unknown` is not read by a timer. See
  `docs/terminal-console.md`.
- The console's leads view draws `-` in the MSG column of a lead that is not
  running (state stopped, ended, desired or unknown, or no epoch), where it
  drew the words of the lead's last epoch's message; `off` stays for a lead
  whose messages are off. See `docs/terminal-console.md`.
- The console's leads view says what settles a lead whose launch is not
  settled. A lead that is `unknown` with the reason `launch-unsettled` or
  `launch-reply-lost` gets the line "launch settles by itself 40 s after the
  start; if not, restart the daemon", the same words answer a stop or a start
  refused with `intent-pending` (which does not tell the owner to read the
  list, because reading it does not settle the launch), and a start refused for
  the limit of running leads while an unknown lead is listed says that the
  unknown lead holds a slot until its launch settles by itself, 40 s after the
  start, and that a restart of the daemon is what is left if it is still
  unknown after that. `docs/scratch-leads.md` has the recovery after a killed
  daemon. See `docs/terminal-console.md`.
- The console's leads view words a refused attach ticket "the holder refused
  the ticket; a writer may already be attached", because the receipt carries no
  reason beyond `ticket-refused` and the holder grants one writer at a time. See
  `docs/terminal-console.md`.
- The daemon in scratch mode settles a launch that is not settled itself when
  the late-start window ends, so a lead left `unknown` by a daemon killed in
  the middle of a start (reason `launch-unsettled`) or by a start whose reply
  was lost (`launch-reply-lost`) needs no daemon restart. A timer per such
  lead waits until the intent's deadline (the late-start window of 30 s and its
  margin of 10 s, on the registry's clock) and asks the lifecycle to settle
  that one role by the rules of the reconcile: with no unit loaded and its
  control group empty, the intent is closed as abandoned and the lead reads
  `stopped`, with a proof, and leaves the `max_running` count. A unit that is
  loaded at the deadline does not settle the lead and is never signalled (the
  launch's own unit is adopted, another is reported and the intent abandoned,
  and the lead stays `unknown`), a manager that does not answer, or a control
  group that is not proven empty, is tried again at growing waits (the lead
  stays `unknown`, with the reason `control-group-not-proven-empty` in the
  second case), and a lead shown `unknown` for `registry-recovered` has no
  timer. The timer starts nothing, never waits for the role's lock, ends on a
  stop, a new start and at shutdown. See `docs/lead-daemon.md` and
  `docs/scratch-leads.md`.
