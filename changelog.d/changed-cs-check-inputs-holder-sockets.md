- `ataniq-canary check-inputs` now counts the holder's two sockets
  (`attach.sock` and `control.sock` in the runtime folder) in the trial of the
  spec, as the holder does. An inputs file whose `never_mounted` list had 60 or
  61 entries was accepted by `check-inputs` and then refused by the holder at
  its first step (`mount_substrings: more than 64 entries`); it is now refused
  by `check-inputs`. This is in a build with the `ataniq_canary` tag only.
