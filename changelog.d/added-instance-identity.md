- Initialize each daemon state root with `ataniqd init --state-dir DIR` before
  running it. The registry records the instance authority and store incarnation.
  Use the same command on a stopped scratch root to migrate its existing journal.
