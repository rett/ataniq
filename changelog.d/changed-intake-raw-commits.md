- The intake no longer lets a lead choose a converter in the owner's process.
  `ataniq-intake review` reads each commit as the raw object the lead wrote
  (`git rev-list` and `git cat-file`, not `git log`) and parses the headers
  itself, so git never converts a message from the charset that the commit's
  own `encoding` header names. A commit with an `encoding` header other than
  UTF-8 (`utf8` and any case of `UTF-8` pass) is refused, with its id and the
  value escaped, and so is a commit object whose headers are not in the form
  git writes (exit status 1; the lead commits the branch again). Commit text
  that is not valid UTF-8 is still shown escaped. A header or subject over
  32 KiB, a commit over 64 KiB, or commits over 8 KiB each on average are a
  limit error. See `docs/lead-intake.md`.
