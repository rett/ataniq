- Stopping a lead's unit now signals its control group by the identity the
  daemon recorded, not the unit by name. SIGKILL writes to the group's
  `cgroup.kill` through the group's open directory, SIGTERM goes to pidfds of
  the processes the group lists, and a group that was removed or replaced is
  never written to. A state without a recorded control group identity is
  refused, and `systemctl kill` is no longer run.
