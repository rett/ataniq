- Make the attended canary run say why a holder ended at its start. The holder
  reports a refused start on its standard error, which is the unit's journal;
  the event log holds only a code. Step 11 now prints the last 40 lines of the
  journal (`journalctl --user --unit UNIT --since` the first start) before it
  fails because the unit is not active or the role is not starting, and step 22
  keeps up to 500 lines of it in the evidence (`out/22-e1-unit-journal.out`,
  with the epoch in its name).
