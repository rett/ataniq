- The holder's counters gain `alt_screen_on` and `alt_screen_off`, the times
  the emulator entered and left the alternate screen. They are listed in the
  status answer, on the `exit` event and in `final.json` with the other
  counters. `alt_screen_on` above zero says the agent drew with a fullscreen
  renderer; both at zero says it drew on the main screen, which the inject
  fence never reads. The session carries them in its own counters, so the
  final record and the `exit` event hold them even though the pane is where
  they are counted.
