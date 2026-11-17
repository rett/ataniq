- Add `ataniq-intake`, a provisional owner command for the reviewed intake of
  a managed lead's work through the exchange mirror, a bare repository in the
  state folder that is the only way out of a lead. `init` creates the mirror,
  `fetch` brings a lead's branches into it, `branches` lists them, `review`
  shows the change and lists under "Needs your sign-off" each path that needs
  its own approval, with its flag kind (and, with `--records`, the gates of
  the verification records of the tip). The kinds are `exec-config` (a path in
  the execution-config set), `symlink`, `submodule`, `aliased` (a changed path
  in a folder that a symbolic link with an execution-config name stands for)
  and `history-only` (a path of one of those kinds in some commit of the
  branch that the net change does not show, such as one added and removed
  again; it still enters the history). `verify` runs
  gates in a sandbox and stores the result in the records folder, and
  `integrate` moves an upstream branch to a reviewed, verified tip,
  fast-forward only. The sign-off is typed as arguments (`--accept-change`,
  and one `--accept-path` for each flagged path); the command never asks a
  question. Everything a lead wrote is shown escaped and labelled "unverified
  lead data". The same `--path-rule` list must be given to `review` and to
  `integrate`. The command does not touch a working checkout, which gets the
  commits by its own fetch from the mirror. No sandbox is shipped or
  qualified: `fetch` and `verify` run the commands of a template file that the
  owner writes, and refuse without a usable one. A verification pass is
  evidence, not proof, and after the sign-off the code runs as the owner like
  any merged code. See [the lead intake guide](docs/lead-intake.md).
