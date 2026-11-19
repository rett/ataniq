- The provisional terminal console can show the daemon's leads:
  `ataniq-console --daemon DIR`, where DIR is the folder that holds the
  daemon's socket, shows the projects with their lead and coordinators and
  each one's state, and asks the daemon again every few seconds. The view is
  read-only: every control stays unavailable with its reason, and there is no
  session terminal in it. With no daemon, a slow one or one that speaks
  another protocol, it shows that plainly with the next step in place of the
  leads, and keeps asking. `--daemon` cannot be combined with `--demo`,
  `--attach` or `--return-key`. The view is provisional and is not a
  qualification.
