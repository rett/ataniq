- Add memory, swap and task limits to the lead unit specification
  (`leadunit.Limits`, `Spec.Limits`). A specification with limits starts its
  unit with `MemoryMax=`, `MemorySwapMax=` and `TasksMax=` set in the start
  call; one without limits is unchanged. The values come from configuration and
  are held to bounds (memory 256 MiB to 64 GiB, swap 0 to 64 GiB where 0 means
  no swap, tasks 16 to 65,536). The in-memory manager records them, and the
  lifecycle's new optional `Options.Limits` function gives the limits of a role
  at each launch.
