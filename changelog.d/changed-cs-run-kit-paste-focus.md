- Tell the owner of the attended canary run two things the console and the unit
  need. At the paste step the script, the page and the guide now say to give the
  session pane the focus first (select it and press Enter): a paste while the
  console has the focus itself is dropped without a word, and the first Enter
  only moves the focus. Between the two epochs of pass A the script now also
  reverts the unit with `systemctl --user revert`, so that the second start does
  not load the `MemoryMax` drop-in of the first. The guide lists three limits it
  does not remove: the script cannot see that the first epoch's console is
  closed, step 12 may read the waiver before the holder has written it, and a
  stop may leave no final record.
