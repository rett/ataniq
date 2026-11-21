- The intake no longer lets a lead choose a converter or a path in the owner's
  process. `ataniq-intake review` reads each commit as the raw object the lead
  wrote (`git rev-list` and `git cat-file`, not `git log`) and parses the
  headers itself, so git never converts a message from the charset that the
  commit's own `encoding` header names. A commit with an `encoding` header other
  than UTF-8 (`utf8` and any case of `UTF-8` pass) is refused, with its id and
  the value escaped, and so is a commit object whose headers are not in the form
  git writes (exit status 1; the lead commits the branch again). Commit text
  that is not valid UTF-8 is still shown escaped. A header or subject over
  32 KiB, a commit over 64 KiB, or commits over 8 KiB each on average are a
  limit error. A header of many continuation lines is parsed in time linear in
  its size, and the call's time limit and context are checked between the
  commits that are parsed. The `gitenv` package no longer allows the `git log`
  subcommand at all, so no owner-side command can convert a message.
  `ataniq-intake fetch` names the source of the fetch to the owner's git as a
  `file://` URL (a `%` in the path is written `%25`; a path with `@[` in it
  cannot be passed so, as git changes it, and a template whose `serve.repository`
  holds one is refused when it is loaded), so git never reads a
  bundle file at the template's `serve.repository` path itself and always starts
  the sandboxed upload-pack, also for a bundle that appears at the path after
  the checks. As a second layer it refuses a template whose `serve.repository`
  path exists on the host (a regular file, a symlink, any folder, or a path that
  cannot be checked), before the measure runs and again before git starts; a
  path that does not exist passes, as does the clone's own path while it is a
  real folder (a clone that was replaced by a symlink or a file in between is a
  hostile tree, exit status 1). The refusal's hint says that the path exists or
  cannot be checked and that the mirror was not changed, which holds for both
  checks. The `qualify-mirror` report gets the fact `repository_on_host` in
  `s16-fetch.clone`. See `docs/lead-intake.md` and
  `docs/qualification-lead-mirror.md`.
