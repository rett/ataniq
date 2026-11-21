- Add `make test-ownerchannel`, which runs the tests of code behind the
  `ataniq_ownerchannel` build tag, alone and together with `ataniq_canary`, as
  a third CI job, and a guard in `tests/` that keeps that tag out of release
  builds: tagged files only in the daemon, its command and the host adapter,
  no command depending on the owner channel's service, journal, host adapter
  or stand-in without the tag, no module used only with the tag, and a
  workflow that names the target and never the tag.
