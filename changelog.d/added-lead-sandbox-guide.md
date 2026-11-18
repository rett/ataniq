- Add `docs/lead-sandbox.md`, a guide to the lead sandbox proposal: the launch
  plan, nested sandboxes under owner decision R17, the file-system view with
  the host layout of a lead and its four parent folders, the clone rules, the
  checks by handle before every start (rule B1), the environment allowlist,
  network and DNS, the probe, the eleven start-up assertions, symlink-safe
  reads, the profile digest and the open points. It is a proposal that awaits qualification and the owner's decision on
  the profile; nothing starts a sandbox in ordinary use, and no command uses it
  yet. Add the opt-in qualification harness `qualify` for the proposal, with its
  guide `docs/qualification-lead-sandbox.md`, and the target `make
  qualification-leadsandbox-build`, which runs the doctor and builds the harness
  and `ataniq-probe`, and starts no sandbox and launches nothing. Both are
  unqualified infrastructure: the harness records facts for a coordinator and
  says nothing about the sandbox being qualified, safe or supported. The
  harness has an opt-in case S21 that plants symbolic links and entries of the
  wrong type at the bind targets in the lead's scratch folders, records that
  each start is refused with the path, puts every path back and launches
  nothing; it needs `--run`, `coordinator_reviewed` and the statement
  `scratch_folders_disposable`.
