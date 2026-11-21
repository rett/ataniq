- Add the console's approvals client for the daemon: `ataniq-console --daemon DIR`
  shows the daemon's approvals when the daemon serves the owner channel. It lists
  the daemon's cards, opens one, reads a path list or a patch larger than one
  response in pages, sends the owner's decision once and shows the outcome the
  daemon gives, and reads and sets the daily limit. The cockpit's approval
  controls and the approvals view follow what the daemon's hello lists. A
  release daemon has no owner channel, so no approval is possible with it: the
  console then says "no owner channel in this build". A lost connection drops
  what was typed and decides nothing, and a decision that got no answer is
  never sent again until the card is opened again. `--no-bell` now also works
  with `--daemon`.
