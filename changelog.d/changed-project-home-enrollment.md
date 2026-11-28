- Existing daemon state roots need a project home set once per project. With
  the daemon stopped, run `ataniqd init --state-dir DIR`, then
  `ataniqd project-home --state-dir DIR --home self PROJECT` on its home
  instance. Use that instance's authority ID on each executor.
