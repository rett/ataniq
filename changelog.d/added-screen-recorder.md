- Add a screen recorder for scratch leads, in a build with the `ataniq_canary`
  tag only, for the owner who makes the fixtures of a new agent version by
  hand: a lead whose scratch config says `"record_screens": true` (optional,
  `false` when absent) is started with `ataniq-holder --record-dir`, and its
  holder writes, raw, into the folder `record` of the lead's runtime folder one
  fixture pair (`NNNN.txt`, `NNNN.json`) per distinct screen that stood still
  for about 300 ms, at most every 500 ms, and the screen shown at the end, each
  window title (`titles.jsonl`) and each OSC 7501 payload (`osc7501.jsonl`).
  The folder is 0700 and its files 0600, with a `README.txt` that says the
  files are raw (scrub before use; do not commit); a start whose folder exists
  is refused, the screens stop at 400 pairs and the recording at 8 MiB, each
  with a line in `LIMIT.txt`, and it never blocks the agent. A recording of
  the login screen holds a login URL and code: delete the folder once the
  fixtures are made. Without the field nothing changes.
