- The console's leads view and its guide now describe when a lead's one message
  per epoch is spent: as soon as the daemon has recorded the message, before
  the request is written to the holder, and not when the request is written. A
  scratch lead gets it back in three cases only: the holder answered not
  delivered, the holder refused the request itself (not busy), or the request
  was never sent because the holder's socket could not be dialled or its write
  deadline could not be set.
