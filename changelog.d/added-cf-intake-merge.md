- The intake can review a merge the host built, in a new merge mode of the
  `internal/intake` package: the base is the target's tip, the tip is the
  merge under a pin in `refs/ataniq/pending/`, and `Integrate` moves the target
  from the one to the other and writes no other ref. It adds a fuller summary
  for the owner's approval card (binary by content, forced-text line counts
  with renames off, new bytes, and lists of binary files, symbolic links, mode
  changes and submodules), an `odd-path` flag for paths the matcher cannot read
  safely, a check of the repository's `info/attributes` before each diff of a
  merge, and a digest of the execution-config rule set. No command uses it yet,
  and the lead-branch review is unchanged.
