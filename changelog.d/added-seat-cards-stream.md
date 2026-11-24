- The seat's card list hears of a change of the waiting cards at once over a
  stream (`POST /api/watch/stream`, server-sent events read with a
  same-origin fetch), instead of at the next answer of its long poll. The
  stream says only that the cards moved, with the version the poll answers;
  the list reloads by the same rule as before (at most once every 20
  seconds, never over typed text). It asks the gate nothing, does not keep
  the session alive, ends with the session, a logout or the seat's
  shutdown, and is reopened about every 25 seconds (its response lasts 25
  seconds at most from its request, its opening included). At most 3 streams
  are open for one passkey, 12 are opened a minute and 32 are open in all,
  and at most 3 openings of one passkey (8 in all) are in progress, bounded
  before the request's body is read. Any
  refusal or failure of the stream, or a browser that cannot read one, falls
  back to the long poll, which the Card page keeps. The stream is closed
  while the page is hidden and reopened when it is shown. It is not a poll
  for the notices: a notice can come while the list is open over it. A proxy
  in front must pass the stream on unbuffered and uncompressed; not yet
  qualified on the deployed path or on a phone.
