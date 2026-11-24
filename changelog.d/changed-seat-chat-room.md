- The seat's Leads pages now have a room of their own at the gate, 6 calls in
  80 seconds (opening Leads, selecting a lead and refreshing it once, with one
  to spare), 2 taken from the reads, which keep 12 for the card lists, 2 from
  the decisions (now 8: 4 decisions in 80 seconds) and 2 from pairing and
  login (now 10: 5 logins); the total stays 60 requests a minute. A Leads page
  past its room says "The desk is busy. Try again in a minute." and never
  takes a card list's read: measured with three card lists reloading every 20
  seconds and a conversation refreshed once in 10 seconds, no list reload is
  busy (42 of 90 were while the Leads pages shared the reads), and the owner's
  visit of the Leads pages once in 80 seconds never is. The seat's log names
  the Leads pages' calls kind `chat`, and its line of counts gains `chat T/R`
  after `read T/R`.
