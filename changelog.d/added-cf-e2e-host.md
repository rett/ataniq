- Add end-to-end tests of the owner channel for a host with no seated lead, in
  a build with the `ataniq_ownerchannel` tag only: the console's approvals
  model, the console's client, the daemon with its owner connection, the
  service, the real journal, the host adapter and a temporary bare mirror in one
  test, with the stand-in for the planner's door and a fake verification suite.
  One request is followed from the file the lead drops to the ref that moves,
  and the cases that need no seat are run on it: a branch that moves or a
  request that is withdrawn with the card open, a target that moves after the
  decision, a decision used twice, expiry, a stop at each crash point and a
  restart with a held decision, the summary of a known change set and of 5,000
  paths read in pages, missing and surplus sign-offs, a wrong digest, epoch,
  revision or id, a merge made by the lead, restarts of the console and the
  daemon, a journal and a door that disagree, attributes in the tree, failed and
  cut verifications, and hostile commit headers. The confirm code is found in
  no place of the host or on the wire, and the plan token only on the card and
  in the journal's copy of the decision record. Cases that need a seated lead
  or the real door stay for step 2b. Nothing changes in a release build.
