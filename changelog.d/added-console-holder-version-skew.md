- The console's leads view has words for the refusal `holder-version-skew`
  of a start, a ticket and a message: the holder and the daemon are other
  builds, so the owner runs the holder and the daemon from one checkout (a
  holder built again, or the daemon restarted with the matching build), and
  restarts a lead that is running. A refused start leaves the lead stopped, so
  starting it again alone is refused again. It also has words for a lead the
  daemon shows `degraded`, with the reason `holder-version-skew`, because its
  running holder is another build: the daemon leaves its unit running, and
  the owner stops the lead, then starts it again.
