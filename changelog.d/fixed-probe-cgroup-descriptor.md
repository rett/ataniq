- Fix `ataniq-probe` and `ataniq-holder` holding the cgroup's `cpu.max` open
  at the lowest free descriptor number (usually 3) when they run in a cgroup
  with the cpu controller, such as a system service. The Go runtime does that
  to size `GOMAXPROCS` from the cgroup's CPU limit; both programs now turn that
  off (`containermaxprocs=0`), so the descriptors the runtime holds are only the
  poller's and a descriptor number given to the probe is never taken by it.
