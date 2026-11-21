- The holder's counters gain `alt_screen_on` and `alt_screen_off`, the times
  the emulator entered and left the alternate screen. They are listed in the
  status answer, on the `exit` event and in `final.json` with the other
  counters. `alt_screen_on` above zero says the agent drew with a fullscreen
  renderer; both at zero says it drew on the main screen, which the inject
  fence never reads. Step 22 of the canary run prints them from `final.json`,
  and step 16 names `alt_screen_on` as the thing the lead reads.
