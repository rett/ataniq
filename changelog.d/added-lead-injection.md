- Add the `internal/inject` packages for fenced message injection into a
  terminal-hosted lead, as a library with no command using it yet. They
  classify a session as idle, working, blocked, ended or unknown for claude
  2.1.325 and codex 0.169 (also spelled 0.169.0) from OSC 7501 reports, window
  titles and screen recognisers built from recorded sessions; any other version
  is refused. The fence pastes a message only into a composer it reads as empty
  and idle, writes nothing while it sees an approval dialog, and presses Enter
  only when a second check after the paste finds the composer idle and holding
  the header, or showing a pasted-text marker that was not there before.
  Durable command states keep a command `uncertain`, and its lead's queue
  frozen, until an operator decides that it was delivered, or drops it, or
  sends it again as a new command; nothing retries. Message admission fixes
  the header, the senders and edges (never `owner`), the text and marker rules,
  and the caps of 16 messages and 64 KiB for a target's queue (a project lead
  or the supervisor), 6 a minute for a sender and 4 KiB for a text. It is
  provisional and unqualified, built from recorded screens of an earlier
  prototype and never run against a real agent: no daemon exists, no holder
  uses it yet, nothing reads delivery from the conversation file, and no live
  agent was used.
  See [lead injection](docs/lead-injection.md).
