- A guide to the outward keys (`docs/outward-keys.md`): the deploy, push and
  signing keys live in the owner's laptop agent with a confirmation for each
  use, forwarded to the development host only for a deploy, push or release
  session; the repository fetches through a read-only route and pushes with
  the laptop's key; commits and tags are signed with a public-key literal
  (`gpg.format ssh`), so the private part stays in the agent; branch and tag
  protection is the owner's setting on the git host.
