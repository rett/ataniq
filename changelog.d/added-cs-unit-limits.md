- Add memory, swap and task limits to the lead unit specification
  (`leadunit.Limits`, `Spec.Limits`). A specification with limits starts its
  unit with `MemoryMax=`, `MemorySwapMax=` and `TasksMax=` set in the start
  call; one without limits is unchanged. The values come from configuration and
  are held to bounds (memory 256 MiB to 64 GiB, swap 0 to 64 GiB where 0 means
  no swap, tasks 16 to 65,536). The in-memory manager records them, and the
  lifecycle's new optional `Options.Limits` function gives the limits of a role
  at each launch; the start checks them with `Limits.Validate` before the
  registry changes and refuses limits outside the bounds like a preflight
  refusal.
  `leadunit-qualify` starts its unit with limits from its flags
  (`--memory-max`, `--swap-max`, `--tasks-max`, `--memory-lowered`; a memory or
  swap size must be a whole number of 4096-byte pages), reads them back from
  `systemctl --user show` and the control group files, lowers the memory limit
  once, and checks that no runtime folder entry is left after the stop; it has
  not been run.
