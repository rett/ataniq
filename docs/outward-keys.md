# Outward keys

Outward keys are the keys that act outside the development host (the AI host):
the authority host's deploy key, the git remote's push key, and the key that
signs commits, tags and releases. The owner's rule for them:

- They live on the owner's laptop, in its ssh-agent, until hardware security
  keys (FIDO2, `ed25519-sk`) replace them; then the private part stays on the
  security key.
- Each is loaded with a confirmation for each use (`ssh-add -c`), a lifetime
  (`-t`) and, for a login key, the hosts it may reach (`-h`). With a FIDO2 key
  the confirmation is the key's touch.
- The agent is forwarded to the development host only for a deploy, push or
  release session, never for an everyday one.
- No private key for the authority host, the git remote or signing lives on the
  development host. Nothing in this repository sets any of it up: these are the
  owner's steps.

The deploy key's procedure, with the forwarding, the authority host's
`authorized_keys` line and how to prove it, is in [the deploy
manual](../deploy/authority-host/DEPLOY.md#the-deploy-key). This page adds git.

The placeholders: `AI-HOST` is the development host and `git@GIT-HOST` the git
host's ssh login, each named as the laptop's `known_hosts` names it; `OWNER/REPO`
is the repository on the git host; `PUSH-KEY` and `SIGNING-KEY` are key files on
the laptop.

## Git: fetch and push

The repository on the development host fetches through a read-only route and
pushes through the laptop's key. A pushurl splits the two on one remote:

```
git remote set-url origin https://GIT-HOST/OWNER/REPO.git
git remote set-url --push origin ssh://git@GIT-HOST/OWNER/REPO.git
git remote -v
```

- **Fetch** is read-only: anonymous https when the repository is public, or else
  a credential that the git host itself limits to reading this repository
  (a read-only token), so that what the development host holds cannot push. It
  is not an ssh key: no private key for the git remote lives there.
- **Push** uses the push key, loaded on the laptop for the git host only and
  forwarded for the session:

  ```
  ssh-add -c -t 30m -h AI-HOST -h 'AI-HOST>git@GIT-HOST' PUSH-KEY
  ```

  To keep ssh from trying a key file of the development host, push with the
  options of the deploy driver, in the repository's own configuration:

  ```
  git config core.sshCommand 'ssh -o IdentityFile=none -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ForwardAgent=no -o ControlPath=none'
  ```

  `git push` then fails without the forwarded agent, and asks on the laptop
  (or for a touch) with it.

## Signing commits and tags

Git signs with ssh keys (`gpg.format ssh`). With the public key given as a
literal, `user.signingkey` names no file: git asks the agent to sign, and the
private part stays in the agent on the laptop.

```
git config gpg.format ssh
git config user.signingkey 'key::ssh-ed25519 AAAA...PUBLIC-KEY signing-key'
git config tag.gpgSign true
```

Load the signing key with a confirmation and a lifetime, but without `-h`: the
agent uses a key with `-h` only to log in, and refuses it for any other
signature.

```
ssh-add -c -t 30m SIGNING-KEY
```

So it is the one key in the forwarded agent without `-h`, and no host takes it
as a login key: keep it out of every `authorized_keys` and off the git host's
list of login keys (register it there as a signing key only). An authority-host
release is signed the same way, from the laptop or in the forwarded session:
`ssh-keygen -Y sign -f SIGNING-KEY.pub -n authority-host-release SHA256SUMS`
uses the agent's key when it is given the public part.

With a FIDO2 key the literal is the `sk-ssh-ed25519@openssh.com` public key and
each signature is a touch.

## Branch and tag protection

Branch and tag protection on the git host is the owner's setting, not this
repository's: no force push and no deletion of `master` and `develop`, and no
moving or deleting a release tag. It bounds what a confirmed push can change.
Nothing here sets it or checks it.
