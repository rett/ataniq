- In scratch mode, the daemon's scan at start of the leads it adopted
  `starting` could, when a lead was stopped and started again while the scan
  ran, start the observer of the old epoch in place of the new epoch's,
  which then ended without looking at the new holder: the lead stayed
  `starting`. An observer is no longer started for a lead that has one of a
  later epoch.
