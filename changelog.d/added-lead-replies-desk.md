- Under the canary tag, a daemon in scratch mode now takes its leads' written
  replies to the owner, read only. Each lead's chat is switched at the desk with
  `ataniqd chat list --config CONFIG` and `ataniqd chat set KEY off|read
  --config CONFIG`, and its quarantine emptied with `ataniqd chat purge KEY
  --config CONFIG` (the operations `chat.list`, `chat.set` and `chat.purge`,
  which the daemon lists after the six lead operations, so the console's check
  of lead control is unchanged, and which the seat gate does not serve); every
  lead is off whenever the daemon starts, a switch takes effect at once, and
  sending is not built (`send` is refused `chat-send-not-built`). A turn-on
  needs one of the four names of the lead's quarantine free, and is refused
  `chat-quarantine-full` (exit 31, with one fixed line) otherwise; the purge
  removes what it can in bounded steps, wide and deep folders over repeated
  runs, never following a link or leaving the quarantine. While a lead's chat is on,
  one scanner of its own takes its replies every 2 seconds, stops with the
  switch, the daemon or a store that fails, and logs counts only. Every
  `lead.define` and `lead.start` writes the reply instructions into the lead's
  empty instruction file while the lead is proven stopped, by an exchange that
  puts back an owner's version saved meanwhile, never over the owner's own
  text; neither touches the reply folder (`/home/lead/.ataniq-replies`), which
  each turn-on of the chat makes afresh. The instructions say that Cards are
  the only path of an approval and a chat message grants no authority, that
  the daemon makes a fresh folder at each turn-on, and that a full folder or
  an odd entry stops chat until the owner turns it on again. A new
  read-only chat port, `leaddaemon.LeadChat`, answers the seat's `seat.chat`
  from the same chats (off leads as bare rows and `chat-off`, a stopped chat or
  a failed store as `chat-busy`, at most 40 calls a minute, and `chat-busy`
  until the daemon binds it). See `docs/scratch-leads.md`, "Lead replies".
