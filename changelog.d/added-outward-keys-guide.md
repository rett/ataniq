- A guide to the outward keys (`docs/outward-keys.md`): the deploy, push and
  signing keys live in the owner's laptop agent with a confirmation for each
  use, forwarded to the development host only for a deploy, push or release
  session, on a connection of its own (no shared connection, whose forwarded
  agent may be another), and torn down when the step is done (`ssh-add -D`
  on the outward agent, then close the login); a confirmation is accepted
  only right after the owner started the step that causes it, and any other
  prompt is refused as an incident; destination constraints (`ssh-add -h`)
  need OpenSSH 8.9 at both ends of every hop, a FIDO2 key 8.2 on the laptop
  and the authority host as well; the repository fetches through a
  read-only route and pushes with the laptop's key; commits and tags are
  signed with a public-key literal (`gpg.format ssh`), so the private part
  stays in the agent; a cutover replaces every exposed key with a new,
  distinct keypair made on the laptop, and withdraws the old ones from all
  three key stores, the git host, the authority host's `authorized_keys` and
  the release signers file `ah-switch` verifies against, checking there that
  the old signing key is rejected and the new one accepted; for git, the old
  signing key is revoked (`gpg.ssh.revocationFile`) and history is verified
  by object ids recorded beforehand, since `valid-before` is judged by the
  signing time the object itself records; branch and tag protection is the
  owner's setting on the git host.
