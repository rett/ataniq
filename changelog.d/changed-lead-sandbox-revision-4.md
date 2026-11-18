- Change the lead sandbox proposal to revision 4 of the lead-hosting design. It
  is still a proposal that awaits qualification and the owner's decision on the
  profile; nothing here is qualified, safe or supported, and nothing uses it
  yet. The layout: every lead has the folder of its working Qargi records
  (`<state>/leads/<role>/qargi`, always bound read-write, at an inside path the
  inputs give) and four parent folders, `/ataniq/requests`, `/ataniq/mailbox`,
  `/ataniq/mail` and `/coord`, which are only bound folders; the git identity
  is bound as the folder that holds it, at `/ataniq/git`; the profile version
  is 4. There are eleven start-up assertions: assertion 11 looks at the four
  parent folders and that no coordinator's tree or home is visible, and
  assertion 7 is checked on both sides. Rule B1 of the design is applied to the
  checks: they walk by handle without following links, from the state root the
  caller owns or, for a source outside it, from the root of the file system. A
  bind source Ataniq supplies now reaches bubblewrap as a descriptor
  (`--bind-fd`, `--ro-bind-fd`): the pass-through step, `ataniq-probe pass-fds`,
  walks to it by handle (`--walk-dir FD ROOT REL`, `--walk-file FD ROOT REL`),
  and a walk it refuses is exit 96 before bubblewrap starts. The step now takes
  at most 128 descriptors in all, no longer 8 files. A row of the host (`/usr`,
  the `/etc` files) is still bound by path. Whether bubblewrap mounts the
  object behind a descriptor is a qualification item, and the harness takes a
  bubblewrap release of 0.10.0 as its assumed minimum. In the harness
  configuration `git_identity_file` is now `git_identity_dir`, a folder that
  holds the file `config` and nothing else, and `qargi_records_inside` is
  required; the evidence files have format 3 (format 2 added the walks and the
  descriptors of the bind sources, format 3 the facts of case S21). The
  harness gains the opt-in case S21, planted bind paths: at every path the start
  walk looks at (the mount points and the tree's `.git` folder) it plants a
  symbolic link to a path outside and then an entry of the wrong type, runs the
  checks of a start, records whether the start was refused, by which check and
  with which problem and path, puts the original entry back and looks at the
  result, in its own step, `planted-bind-paths`, before the probe spec. It
  never launches (a run whose only case is S21 skips the launch phase), and it
  needs `--run`, `coordinator_reviewed` and the new configuration key
  `scratch_folders_disposable`, the statement that the folders under the state
  root are disposable scratch folders and not a live lead's; a dry run does
  nothing for it and says so. Its results are `planted-paths-refused`,
  `planted-paths-refused-other-runtime` (the other runtime's start, by the walk
  alone, at the paths in folders the configuration names) and
  `planted-paths-restored`, each ok, failed or not-checked; its facts are
  `planted_paths` in `summary.json` (evidence format 3). It is made from
  outside, as a stand-in for a lead that plants from inside. `AGENTS.md` names
  the qualification build, `make qualification-leadsandbox-build`.
