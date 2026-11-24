- The seat gate has a read-only view of the leads' conversations, served in
  the phone trial only from STAND-IN samples: the gate operation `seat.chat`
  under the capability `chat`, with the verbs `leads` (up to 8 leads, each
  with its chat switch `off`, `read` or `send`, a state word, an epoch and a
  head; an off lead's row carries nothing but its name and switch) and `read`
  (one page of a lead's retained conversation after a sequence number, at
  most 16 entries, paged by encoded bytes within 16 KiB, with `more` and
  `reset`). No verb sends. An entry's text is up to 2000 bytes of plain text
  (line feeds allowed; other controls and bidirectional and format controls
  refused), a lead's text is untrusted and shown to the owner only, and a
  shortened entry is marked `cut`. The refusals `chat-off`,
  `chat-unknown-lead` and `chat-busy` are not counted by the refusal bound.
  `ataniqd run --owner-channel --trial --seat-gate ADDR` gives its gate the
  STAND-IN (`internal/chat/standin`: three sample leads and conversations
  held in memory, every result marked `stand_in`), and its start line ends
  `... genesis and chat` followed by a line that says the chat is the
  STAND-IN's; every other gate lists no `chat`. No lead, conversation file or
  other port of the daemon is read or reached. The seat's lead pages are not
  built yet.
