- Add the console's approvals client for the daemon, in the build with the
  owner channel only (the `ataniq_ownerchannel` build tag, never in a release):
  `ataniq-console --daemon DIR` then lists the daemon's cards, opens one, reads
  a path list or a patch larger than one response in pages, sends the owner's
  decision once and shows the outcome the daemon gives, and reads and sets the
  daily limit. A daemon with no owner channel gives "no owner channel in this
  build". A lost connection drops what was typed and decides nothing, and a
  decision that got no answer is never sent again until the card is opened
  again. Release builds are unchanged.
