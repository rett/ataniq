- The owner channel (tagged build only) now says why it refuses a merge whose
  feature has a commit with an `encoding` header other than UTF-8: the request
  is still refused `request-invalid` with no card, but the reply the lead reads
  and the request's status carry the line "a commit declares an encoding other
  than UTF-8" instead of "the host could not review the merge". The line is
  fixed and repeats neither the commit's id nor the header's value; the lead
  commits the branch again in UTF-8. See `docs/lead-daemon.md`.
