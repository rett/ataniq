- The seat's Leads pages follow the chat hint over the card list's stream
  (`POST /api/watch/stream`, opened with `{"leads":"<cursor>"}`), which
  carries one kind of event, `chat-changed`, with the cursor alone: never a
  lead, a count or a text, and nothing of a lead whose chat is off. A
  conversation open at its newest message shows new replies by itself, read
  again through its ordinary page at most once every 40 seconds (2 calls of
  the Leads pages' 5 in 80 seconds) and only while shown, never over typed
  text or an open dialog; scrolled away or on older messages it shows "New
  replies." with a link **Show newest**, and the Leads page shows "Leads
  changed." with **Refresh**. Nothing takes the focus. The stream asks the
  gate nothing, shares the card list's limits (3 streams of one passkey,
  card lists and Leads pages together, 32 in all), takes no notices'
  foreground lease and counts no card as seen. With no live hint (a gate
  without `wait-chat`, a lost link) or on any failure the page says "Not
  live" and keeps its Refresh link; there is no poll of the chat. In the
  phone trial the samples never change, so the pages never move. Not yet
  qualified on the deployed path or on a phone.
