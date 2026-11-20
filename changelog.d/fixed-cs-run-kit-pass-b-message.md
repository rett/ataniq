- Reword the message of the attended canary run so that a live agent writes the
  value only: the file `canary-token.txt` in the top folder of the repository
  holds exactly one line, the value after the label on the next line, then a
  line feed. The `canary-token: ` line stays for the stand-in and the daemon's
  admission. When the gate of step 20 fails, the script now also prints the
  first 3 lines the file holds, so that the wrong reading is seen; the gate
  itself is unchanged.
