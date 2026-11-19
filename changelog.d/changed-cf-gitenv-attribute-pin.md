- Owner-side git reads now ignore attributes from any tree for diffs, logs and
  merges: the pinned git environment sets the attribute source to the empty
  tree for `diff`, `diff-tree`, `log` and `merge-tree`, so a `.gitattributes`
  a lead committed can no longer mark a file as binary in a diff or change
  how a merge joins two sides. `log` now gets `--no-ext-diff` and
  `--no-textconv` as `diff` does, so a patch in a log starts no textconv
  program named in the repository's config. The environment also accepts
  `merge-tree` in one form only (`--write-tree`, the output flags
  `--name-only`, `--no-messages` and `-z`, `--end-of-options` and two full
  object ids, so no option can change what is merged), and `hash-object` in
  the one form that writes a commit, for a merge built by the host, and gains
  a check that refuses a repository whose own `info/attributes` is not empty,
  since git reads that file whatever the attribute source is. Provisional:
  shown by tests on the installed git only, and not part of any
  qualification.
