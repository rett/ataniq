- Change step 14 of the attended canary run so that the owner is not racing the
  30 seconds of the attach ticket. The script now prints the exact console
  command, with its real folder and ticket file, before it mints; the owner
  starts the console first (it waits for a ticket) and presses Enter, and only
  then does the script mint, check that the ticket went to the file it printed,
  and watch that file. The old question whether the used ticket was refused is
  gone, because the console empties the file and the second console never
  presents the ticket: in the first epoch the owner now starts the console once
  more in a fourth terminal, sees that it does not attach and says "waiting for
  a ticket", and the script reads that the ticket file is empty and that the
  holder's event log shows no second attach. The owner is told to close the
  first epoch's console before the second epoch, because a console left open
  takes the next ticket. The page, the preflight's READY text and the guide say
  the same.
