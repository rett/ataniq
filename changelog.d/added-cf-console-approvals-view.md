- The provisional terminal console has an approvals view. `g` opens it from
  console focus (never from session focus, where every key goes to the
  session), and Enter on the pending gate in the right pane opens the oldest
  waiting card. The view lists the waiting cards, the recent decisions and the
  standing rules, shows a card with its plan, diff summary, flagged paths and
  evidence, and opens the full patch in a pager. `L` in the list opens the
  daily card limit of the project under the cursor. A decision is made only on
  the card: an approval needs a code typed from the card's title, a denial
  needs no code; while a field is open every key goes to the field, and Esc
  goes back one step at a time. The pending gate, the timeline (asked,
  decided, applied) and the status line (waiting cards and the age of the
  oldest) follow the cards. When you return
  after a while the console shows a short summary of what happened while you
  were away, and rings the terminal's bell for a card that arrives while you
  are away; `--no-bell` turns the bell off. In this release only `--demo` has
  cards: they are sample cards, the header says "sample cards: a decision
  changes nothing", and a decision on one changes nothing outside the console.
  No approval is possible in a release build: without `--demo` the approvals
  view says "no owner channel in this build". The view is provisional and
  claims no qualification.
