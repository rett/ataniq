- Make the attended canary run easier on the owner, without the script ever
  answering for him. A step that waits for something he starts or stops in
  another terminal (the daemon at steps 10 and 26, the console's ticket at step
  14, the listeners at steps 6 and 24) now asks for it every second for up to 60
  seconds instead of failing at once. Step 26 says when the daemon still answers
  or a root still exists and asks again, up to 5 more times, before it fails.
  `canary-prepare.sh` writes `answers.hint`, suggested paths that the run shows
  as "(Enter for: PATH)" at its four path prompts and takes for an empty line (a
  typed path wins; no word such as `yes` or `read` is ever a hint), and
  `CHECKLIST.txt`, one page with the real paths and what each prompt expects;
  `--template-review`, `--unit-report` and `--plan-summary` name the files to
  suggest. A wrong file is refused before it is recorded and asked again up to
  3 more times: a qualify-mirror report where the template's review is asked
  for, or a report of another template. The new `canary-preflight.sh --root DIR`
  is a read-only look before the run: one line per check, `ok:` or `FIX:` with
  the command to type, and `READY` with the three things to type or `NOT READY`.
  The guide is `qualification/canary/run/README.md`.
