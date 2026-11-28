- Scratch and canary leads record an inject before sending it. The inject-sent
  line, which now carries an effect id that no restart of the daemon repeats, is
  written before the request frame, and nothing is sent when it cannot be
  written: the inject is refused as unavailable and spends nothing. A daemon
  killed in between can no longer have a message delivered that its evidence
  does not know. A holder socket that cannot be dialled leaves an
  `inject-refused` line with the code `unsent`, which frees a scratch lead's
  message; the canary's message stays spent.
