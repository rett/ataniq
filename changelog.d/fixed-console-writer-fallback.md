- A writer attach from the leads view is now pinned to the lead and epoch of
  the selected row, as a viewer attach is, and so is the viewer it becomes when
  it loses the writer role: if another epoch answers, it stops and asks the
  owner to select the lead again, instead of attaching to it. Quitting the
  console while that viewer fallback is asking the daemon for a ticket now
  cancels the request at once, instead of waiting for it to time out, so the
  lead is not left with an unused viewer ticket outstanding.
