- Every existing daemon state root needs `ataniqd init` once. Back it up with
  the daemon stopped. Older builds cannot open the journal after init. The
  daemon refuses hard-linked journals and journals replaced while opening.
  Scratch preflight now reports identity refusals with the daemon's exit code.
