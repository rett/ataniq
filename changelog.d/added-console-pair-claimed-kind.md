- The console's pair-a-phone view shows, under the eight words and before the
  owner confirms, what the phone's authenticator claimed when its backend gives
  it: "Claimed, not proven: vault passkey, synced." The line uses the view's own
  words for the known kinds and backup states, and a change of the claim drops
  the confirm question. A confirmed key of a known kind reads "Paired. Kind,
  claimed: vault passkey." A key paired without a claim reads "Paired. Kind:
  unverified (no claim made)."
