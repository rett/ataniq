- The console's leads view says plainly what became of a message. A done
  message is drawn from the holder's three words (outcome, reason, written):
  "NOT delivered: nothing was written" with the reason (the lead's screen is
  not idle or is blocked, the program version is untested, the text was
  refused), "the message was submitted", or "result unknown" with what is known
  of what was written, and each says that the epoch's one message is spent,
  which is what the daemon does once it has written the request to the holder.
  Before, a not-now answer was drawn as "the result is not known to this
  console". The MSG column agrees: `free`, `free (not delivered)`, `sent` (only
  when the words say it was submitted), `sent-unsure` (something may have
  reached the lead, the words are not known, or the daemon holds the epoch
  spent with none) or `spent, not delivered`. A lead whose state is `ready` with the reason
  `holder-ready` gets a line that says the holder is up and the agent started
  and that its login is not observed. See `docs/terminal-console.md`.
