- Fix two details of the attended canary run kit. In the second epoch the run
  now prints the console command with `--viewer`, matching the read-only ticket
  it mints. Step 12's wait no longer takes an empty or failed answer from
  `systemctl` for the unit's end: it asks again within the same asks. The
  first-time prepare message for a non-empty root no longer gives a delete
  command for a folder that does not carry the prepare mark.
