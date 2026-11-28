- The console's leads view is now viewer-only unless it is started with
  `ataniq-console --daemon DIR --scratch-writer`. Without the flag the hint has
  no `[w]` and says "writer off: start with --scratch-writer", the `w` key
  sends nothing, and the console never asks the daemon for a writer ticket. With
  the flag, `w` is offered for scratch leads only, when the daemon lists the
  writer role in `ticket_roles` or, with no `ticket_roles`, the whole legacy
  scratch interface; a hello without `ticket_roles` no longer offers a writer by
  itself. A writer attach made from the view becomes a viewer for good when it
  loses the writer role, with viewer tickets from the daemon.
