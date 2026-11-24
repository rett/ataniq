- Under the canary tag, a daemon in scratch mode now takes its leads' written
  replies to the owner, read only. Each lead's chat is switched at the desk with
  `ataniqd chat list --config CONFIG` and `ataniqd chat set KEY off|read
  --config CONFIG` (the operations `chat.list` and `chat.set`, which the daemon
  lists after the six lead operations, so the console's check of lead control
  is unchanged, and which the seat gate does not serve); every lead is off
  whenever the daemon starts, a switch takes effect at once, and sending is not
  built (`send` is refused `chat-send-not-built`). While a lead's chat is on,
  one scanner of its own takes its replies every 2 seconds, stops with the
  switch, the daemon or a store that fails, and logs counts only. Every
  `lead.define` and `lead.start` makes the lead's reply folder
  (`/home/lead/.ataniq-replies`, mode 0700, refusing one that is not private)
  and writes the reply instructions into the lead's empty instruction file
  while the lead is proven stopped, never over the owner's own text. A new
  read-only chat port, `leaddaemon.LeadChat`, answers the seat's `seat.chat`
  from the same chats (off leads as bare rows and `chat-off`, a stopped chat or
  a failed store as `chat-busy`, at most 40 calls a minute, and `chat-busy`
  until the daemon binds it); the owner channel does not give it to the seat
  gate yet. See `docs/scratch-leads.md`, "Lead replies".
