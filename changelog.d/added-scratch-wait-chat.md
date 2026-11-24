- A scratch daemon's gate (`ataniqd run --scratch CONFIG --owner-channel
  --seat-gate ADDR`, both tags) lists `wait-chat` too: its chat cursor covers
  the leads whose chat the desk set to `read` and that a scan has not stopped
  (each lead's switch generation and head), sampled together as one state of
  the desk, and moves when such a lead gets a new entry (a reply, or the line
  that says replies were dropped) or a lead's chat is turned on or off; a
  lead whose chat is off moves nothing, and a reply that arrives while it is
  off leaves no gap in its sequence numbers. Its start line ends
  `... genesis, chat and wait-chat`.
