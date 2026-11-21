- A scratch lead's message that was not delivered no longer spends the epoch's
  one message. When the holder answers `not-now` or `refused` with nothing
  written, or refuses the request itself (`disabled`, `wrong-epoch`, `busy` or
  `bad-request`), the epoch's message stays free, `lead.list` shows
  `message_spent` as `false` with the answer still in `message_result`, and the
  lead may send another message in the same epoch (the same message id is not
  refused). Everything that may have reached the lead stays spent: a submitted
  or uncertain result, no answer, a closed connection, an answer that does not
  decode, a refusal code the daemon does not know and a result or refusal that
  could not be written to the evidence. A restarted daemon reads the evidence
  back and keeps the epoch free only when the message has its `inject-result`
  line with `not-now` or `refused` and nothing written, or its `inject-refused`
  line with one of the holder's four refusals; a daemon killed after the `inject-sent` line
  and before the result line leaves the epoch spent, as does any result line
  without a matching `inject-sent` line or one that comes twice. The canary's
  rule that every result spends the message is unchanged. The console's leads
  view says `free (not delivered)` in the MSG column for such a message, its
  receipt line says the epoch's message is free, and `m` sends another;
  `spent-not-delivered` becomes `spent, not delivered` and is seen only when
  the daemon holds the epoch spent after a message that was not delivered,
  because its result line could not be written. See `docs/scratch-leads.md` and
  `docs/terminal-console.md`.
