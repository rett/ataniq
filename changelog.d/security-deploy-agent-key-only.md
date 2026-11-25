- The authority host's `deploy.sh` logs in with the checked ssh agent's key
  only, isolated from the development host's ssh configuration: every ssh and
  rsync runs with `-F /dev/null` (no `IdentityFile`, `IdentityAgent`,
  `ProxyJump` or alias of a config applies) and explicit options: batch mode,
  no key file, `IdentityAgent=SSH_AUTH_SOCK`, public-key authentication only
  (no password, keyboard-interactive, GSSAPI, host-based or PKCS#11), no proxy,
  no shared connection (`ControlMaster=no`, `ControlPath=none`), no agent
  forwarded further, and `StrictHostKeyChecking=yes` with only the known-hosts
  file the owner names. The destination comes from the owner's environment:
  `AUTHORITY_HOST_SSH` must now be `USER@HOST` (an ssh alias no longer works),
  `AUTHORITY_HOST_KNOWN_HOSTS` is required, and `AUTHORITY_HOST_PORT` sets a
  port other than 22. Except for `--self-test`, which prints the options, it
  refuses when the known-hosts file is missing, or `SSH_AUTH_SOCK` is unset or
  its agent holds no key, and `--dry` ends with a reminder that the use must
  have popped a confirmation on the laptop. DEPLOY.md has the owner's
  procedure, "The deploy key": kept on the laptop and loaded with
  `ssh-add -c -h -t`, the agent forwarded for the deploy session only, a
  `restrict,from=` line on the authority host, how to prove it (the negative
  check tests each identity under review, one key file at a time, on the
  driver's own connection: the same destination, port and known-hosts file,
  no ssh configuration, no proxy and no agent), and the same with a FIDO2
  key; the git push command of `docs/outward-keys.md` uses the same options.
