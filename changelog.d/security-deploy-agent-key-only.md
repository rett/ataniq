- The authority host's `deploy.sh` logs in with the ssh agent's key only:
  every ssh and rsync runs with `-o BatchMode=yes -o IdentityFile=none
  -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no
  -o ForwardAgent=no -o ControlPath=none`, so no key file of the development
  host is used and every step is a new login. Except for `--self-test`, which
  prints the options, it refuses when `SSH_AUTH_SOCK` is unset or its agent
  holds no key, and `--dry` ends with a reminder that the use must have popped
  a confirmation on the laptop. DEPLOY.md has the owner's procedure, "The
  deploy key": kept on the laptop and loaded with `ssh-add -c -h -t`, the
  agent forwarded for the deploy session only, a `restrict,from=` line on the
  authority host, how to prove it, and the same with a FIDO2 key.
