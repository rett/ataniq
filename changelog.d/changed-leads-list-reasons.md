- The scratch leads' `lead.list` rows carry two new members, always present:
  `state_reason`, the reason code the registry shows with the shown state (so
  a promoted lead reads `ready` with `holder-ready`, from the daemon), and
  `message_result`, the holder's three words (`outcome`, `reason`, `written`)
  about the current epoch's message, empty when the message was not sent or the
  answer is not known (the daemon keeps it while it runs and reads it from the
  command table after a restart while the message's receipt is the lead's last).
  The summary receipt of a row now leaves out its `message` as well as its
  `result`, which keeps the largest list under 8 KiB. See
  `docs/scratch-leads.md`.
