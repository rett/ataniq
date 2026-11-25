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
  stays in the agent; a cutover withdraws the trust in old push, signing and
  deploy keys for new operations, after checking the fingerprints of all
  three; branch and tag protection is the owner's setting on the git host.
