- Bound what the console's session pane keeps of a session's output, in the
  patched `x/vt`. A cell keeps at most 64 bytes of a grapheme cluster, cut at
  a character boundary; the rest is dropped and counted, and a megabyte of
  combining marks costs bounded memory and linear time. Only a fixed table
  of known terminal modes is recorded (the ones the emulator acts on, the ones
  the pane and console follow, and the ones a full-screen agent commonly sets);
  setting or resetting any other mode number is ignored and counted, a request
  for it is answered "not recognised", and setting 100 000 distinct numbers
  does not grow memory. A hyperlink whose URL is over 2 KiB or whose
  parameters are over 256 bytes is dropped and counted, and the text after it
  has no link; a screen (the main and the alternate each) keeps at most 256
  distinct live links, and text printed under a further link is drawn without
  it and counted; and the data of an OSC, DCS or APC string is cut at 64 KiB
  (it was 4 MiB), with the rest dropped and counted. The emulator exposes the
  counts of every cap it hit through `Counters`.
  Clearing or resetting the screen, switching the alternate screen and
  scrolling a whole region cost the lines they change and no longer every
  cell, so a program that does these in a loop (for example a full-screen
  program that clears the screen on every frame) no longer stalls the pane for
  seconds or minutes, and the pane feeds the emulator in slices of 4 KiB, so
  that a draw waits for one slice of output at most. Output that scrolls a
  region with left and right margins set can still take about a minute for
  64 KiB on the largest screen; see `third_party/patched/x-vt/NOTICE.md`.
