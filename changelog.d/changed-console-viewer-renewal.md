- A viewer attach from the leads view asks the daemon for a new viewer ticket
  each time it connects, so a viewer that the holder dropped connects again
  without the owner asking. It is pinned to the lead and epoch of the selected
  row and stops, asking the owner to select the lead again, when the epoch
  changed or the lead is gone. A busy holder or a refusal is shown in the
  status, and an unreachable daemon is waited out. The writer attach is
  unchanged.
