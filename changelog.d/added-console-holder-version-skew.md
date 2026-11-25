- The console's leads view has words for the refusal `holder-version-skew`
  of a start, a ticket and a message: the holder and the daemon are other
  builds, so the owner runs the holder and the daemon from one checkout (a
  holder built again, or the daemon restarted with the matching build), and
  restarts a lead that is running. A refused start leaves the lead stopped, so
  starting it again alone is refused again.
