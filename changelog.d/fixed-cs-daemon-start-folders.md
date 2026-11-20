- `ataniqd canary start` now refuses, before it changes anything, when the
  holder's state folder or the folder of the role lock
  (`<state root>/roles/<project>`) is missing or not private, as it already
  did for the holder's runtime folder. Each has its own fixed refusal text and
  the path goes to the daemon's log only. Before, the start answered
  `starting` for a unit that ended at once, and the reason was in the journal
  alone. This is in a build with the `ataniq_canary` tag only.
