- The seat's link asks for the chat hint: its hello lists `wait-chat`, and its
  one watcher sends the extended `seat.wait` to a gate that lists `wait-chat`
  and `chat` (the plain one to any other, and to one that refuses the
  extended params), with no call or room beyond the waits'. The hint (the
  cursor and the connection it came on) is dropped on a lost link, a new
  connection, a gate that no longer lists it, an answer without a cursor and
  any failed wait. The seat's pages do not use it yet.
