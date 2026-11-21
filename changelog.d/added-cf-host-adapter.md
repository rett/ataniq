- Add the host adapter of the owner channel, in a build with the
  `ataniq_ownerchannel` tag only: the service's interfaces joined to the real
  merge builder, the intake (merge-mode review, summary, one ref transaction
  with the old value, no checkout and no hook), the journal in the daemon's
  state folder, and a verifier that keeps each run as a verification record.
  A merge the host built stays pinned under `refs/ataniq/pending/`, so the
  mirror's own maintenance cannot remove it or its parents; the adapter adds no
  maintenance. A request enters through a file-drop collector that exists for
  the scratch pass only: a request file in a private folder, refused before
  anything is built when it is not a request or its feature name is outside the
  grammar, with a reply file beside it as the only reply this build writes.
  `ataniqd run --owner-channel` now builds the host's side of a card from
  `owner/scratch.json` in the state folder, over a stand-in for the planner's
  door that keeps nothing across a restart. It is provisional and unqualified,
  and no suite runner is installed yet, so the flag is still refused until one
  is. Release builds contain none of it.
