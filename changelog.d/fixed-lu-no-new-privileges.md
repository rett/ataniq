- The lead unit no longer sets `NoNewPrivileges`. With it, the command that
  pasta runs in its user namespace has no `CAP_SETUID`, so the nested
  bubblewrap could not set up its uid map and the sandbox start was refused.
  The agent keeps no-new-privs, because bubblewrap sets it on the command it
  execs.
