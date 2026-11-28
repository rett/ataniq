- Quitting the console while a writer attach that lost its role is asking the
  daemon for a viewer ticket now cancels that request at once, instead of
  waiting for it to time out, so the lead is not left with an unused viewer
  ticket outstanding.
