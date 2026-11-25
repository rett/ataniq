- AP14 level 1 for an AI host (`deploy/ai-host/README.md`): the owner takes
  passwordless and cached sudo away from the everyday user by hand, from the
  guide's steps: the edits with `visudo -f` (remove the `NOPASSWD:` tag in
  place, keep one password-asked grant, never delete a rule), the drop-in
  `/etc/sudoers.d/zz-ap14-level1-NAME` (`Defaults:NAME timestamp_timeout=0`,
  checked with `visudo -c`) and the root-owned standing-grants register
  `/etc/ataniq/grants`, one fresh root-owned file per grant; a register
  folder found not root's or writable by others is set right in place, never
  moved or replaced.
  `deploy/ai-host/ap14-level1.sh --check --user NAME` is the read-only check
  of the result, run by the owner as root from a copy installed as root's own
  file: it runs `sudo -n -l -U NAME` once and judges only what sudo prints
  (a `NOPASSWD` command; `targetpw`, `rootpw`, `runaspw`, `!authenticate` or
  `exempt_group`, global or command-specific, judged in every word of a
  setting so that a quoted value with blanks is read; a non-zero
  `timestamp_timeout`; no `timestamp_timeout=0`), checks the register and every folder above it by
  stat rules, and prints plain steps for the owner, never an edit command. It
  changes nothing. It stops, never clean (exit 3), on a failed or unknown
  sudo output, anything on sudo's error output, an unreadable register, and a
  user with no sudo grant until the owner answers that root can log in
  another way (`--other-root-login`). Verification is `--check` again and the
  owner's `sudo -k; sudo -n true` in a new login, whose limit the guide states.
  Groups, polkit, doas, setuid files and root-run paths are a checklist for the
  owner; the user's own shell start files and `PATH` folders are a known limit
  of level 1. `--self-test` runs unprivileged and judges saved `sudo -l`
  outputs and register listings in place, never running sudo and writing
  nothing. Agents run only `--self-test` and the Go tests.
