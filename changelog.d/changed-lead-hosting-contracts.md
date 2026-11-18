- Amend the runtime and client protocol contracts for a lead hosted as a
  terminal program (owner decision of 2026-11-17). Its `ready` state is an
  Ataniq observation of the hosted terminal, never a provider report: the
  holder runs for the current epoch, the stream is open, the runtime version is
  the pinned one, login is checked or says `login: not checked`, and the screen
  is an idle composer. The screen then gates each delivery and never proves
  health, authority or completion. Its attach stream is a ticketed terminal
  data plane, not a second command path: Ataniq admits no command, decision or
  grant from it. It has one writer, one viewer and no takeover, accepts a peer
  of the holder's own OS user only, and carries the stated same-user limit.
  The amendment is a specification. Beside it, `ataniq-holder`,
  `ataniq-scratch` and `ataniq-console --attach` build the attach stream for a
  scratch agent only, with tickets from a development stand-in and not from a
  typed command. Nothing sets `ready`: the holder reports the idle
  classification as `unknown` and does not use the screen recognisers of
  `internal/inject`. None of it is joined to the daemon, and none is qualified.
