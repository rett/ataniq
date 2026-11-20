- The lead unit no longer sets `NoNewPrivileges`. With it, the command that
  pasta runs in its user namespace has no `CAP_SETUID`, so the nested
  bubblewrap could not set up its uid map and the sandbox start was refused.
  Bubblewrap sets no-new-privs on the agent it execs, as observed with
  bubblewrap 0.13.1; no probe assertion checks that yet, and a later one
  should. The holder, pasta and the probe's pass-through step now run without
  no-new-privs (they are Ataniq's own code and distro programs that are not
  set-uid).
