- A seat conversation opened without a page cursor (`/leads/daemon/KEY`, the
  directory's link, **Refresh** and **Jump to newest**) now ends at the
  lead's latest message: when long messages did not all fit on the page, or
  replies arrived while it was read, it reads on toward the latest message,
  at most twice more (up to 4 of the Leads pages' 5 calls in 80 seconds),
  instead of stopping short with **Show later**. **Jump to newest** links to
  that address rather than to a window counted back from the head, which the
  same limit could cut short. A further read that meets a busy desk keeps
  the page read so far.
