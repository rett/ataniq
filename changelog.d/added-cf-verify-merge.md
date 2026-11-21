- The intake's verification has a merge mode, for the merge the host builds
  under `refs/ataniq/pending/`: the request and the stored record carry the
  merge and the base (the target's tip) it was built against, the verification
  script refuses to run a gate unless the fetched commit is a merge of two
  parents whose first is the base (status 93), and `Integrate` of a merge
  counts only merge-mode records of that merge on that base. Records of a lead
  branch are unchanged and still read. No command uses it yet.
