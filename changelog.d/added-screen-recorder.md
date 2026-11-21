- Add a screen recorder to the holder, in a build with the `ataniq_canary` tag
  only: `ataniq-holder --canary --record-dir DIR` writes, raw, one fixture pair
  (`NNNN.txt`, `NNNN.json`) per distinct screen of the agent at most every
  500 ms, each window title (`titles.jsonl`) and each OSC 7501 payload
  (`osc7501.jsonl`) into DIR, which must be the folder `record` in the holder's
  runtime folder and must not exist. The folder is 0700 and its files 0600,
  with a `README.txt` that says the files are raw (scrub before use; do not
  commit); a recording stops at 400 pairs or 8 MiB with a line in `LIMIT.txt`,
  and never blocks the agent. Without the flag nothing changes.
