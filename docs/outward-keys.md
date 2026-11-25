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
  release session, never for an everyday one, on a connection of its own
  (`ControlMaster=no`, `ControlPath=none`), and torn down when the step is
  done: `ssh-add -D` on the outward agent, then close the forwarded login.
- A confirmation is accepted only right after the owner started the step
  that causes it; any other prompt is refused and treated as an incident
  (see [which confirmations to
  accept](../deploy/authority-host/DEPLOY.md#the-deploy-key), step 3).
- No private key for the authority host, the git remote or signing lives on the
  development host. Nothing in this repository sets any of it up: these are the
  owner's steps.

The deploy key's procedure, with the forwarding, the versions it needs, the
confirmations to accept, the authority host's `authorized_keys` line and how
to prove it, is in [the deploy
manual](../deploy/authority-host/DEPLOY.md#the-deploy-key). This page adds
git, and the cutover from keys kept anywhere else.

The placeholders: `AI-HOST` is the development host and `git@GIT-HOST` the git
host's ssh login, each named as the laptop's `known_hosts` names it; `OWNER/REPO`
is the repository on the git host; `PUSH-KEY`, `SIGNING-KEY` and `DEPLOY-KEY`
are key files on the laptop, and `OLD-KEY` an exposed one; `PRINCIPAL` is the
switch policy's `principal`, and `DEPLOYUSER` the authority host's deploying
user; `/KNOWN-HOSTS-FILE` is a known-hosts file you keep on the
development host with the git host's key, checked against the fingerprint the
git host publishes.

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

  To keep ssh from using anything of the development host but the forwarded
  agent, push with the options of the deploy driver, in the repository's own
  configuration (one line):

  ```
  git config core.sshCommand 'ssh -F /dev/null -o BatchMode=yes -o IdentityFile=none -o IdentitiesOnly=no -o IdentityAgent=SSH_AUTH_SOCK -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o GSSAPIAuthentication=no -o HostbasedAuthentication=no -o PKCS11Provider=none -o ProxyCommand=none -o ProxyJump=none -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=/KNOWN-HOSTS-FILE'
  ```

  No ssh configuration file is read (`-F /dev/null`), so no `IdentityFile`,
  `IdentityAgent` or `ProxyJump` of yours applies; only the agent of
  `SSH_AUTH_SOCK` signs, only with a public key, never asking on the
  development host's terminal (`BatchMode=yes`), over a new connection each
  time, to the host key of your known-hosts file alone. `git push` then fails
  without the forwarded agent, and asks on the laptop (or for a touch) with
  it.

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

## The cutover: from old keys to the laptop's

Loading the laptop's keys does not make an older copy useless: a push key, a
signing key or a deploy key that lived on the development host (or anywhere
else an agent session could read it) still works wherever it is trusted,
until that trust is withdrawn. Such a key is exposed: it is replaced, never
moved to the laptop. Make the cutover once, for all three keys, before the
first session that uses the laptop's:

1. **Make new, distinct keypairs on the laptop.** One for each role, made
   fresh there: `ssh-keygen -t ed25519 -f PUSH-KEY`, `ssh-keygen -t ed25519
   -f SIGNING-KEY` and `ssh-keygen -t ed25519 -f DEPLOY-KEY` (or the
   `ed25519-sk` forms). Never copy an exposed key to the laptop, and never
   use one keypair for two roles.
2. **List what is trusted now, in every key store.** There are three:
   - the git host: its list of login keys (and deploy keys) for the account
     and the repository, and its list of signing keys;
   - the authority host's login: from your root session,
     `ssh-keygen -lf /home/DEPLOYUSER/.ssh/authorized_keys`;
   - the authority host's release signers: the file the switch policy names
     (`allowed_signers` in `/etc/authority-host/switch-policy.json`, for
     example `/etc/authority-host/allowed_signers`), which `ah-switch`
     verifies every signed release against, for the policy's `principal`
     (see [the switch policy](../deploy/authority-host/README.md#the-switch-policy)):
     `ssh-keygen -lf /etc/authority-host/allowed_signers`.

   Beside them, the repository's allowed-signers file
   (`gpg.ssh.allowedSignersFile`), if there is one, decides what
   `git verify-commit` and `git verify-tag` accept.
3. **Check the fingerprints of all three keys.** On the laptop,
   `ssh-keygen -lf PUSH-KEY`, `ssh-keygen -lf SIGNING-KEY` and
   `ssh-keygen -lf DEPLOY-KEY` (or the `.pub` files): three different
   fingerprints. Each store of step 2 must end up holding the new
   fingerprint where it belongs (the push key as a login key on the git host;
   the signing key as a signing key only on the git host, in the release
   signers file under the policy's principal and in the repository's
   allowed-signers file; the deploy key in the deploying user's
   `authorized_keys` only) and no other outward key.
4. **Withdraw the old trust.** Remove every old key from those stores: the
   old push key from the git host's login and deploy keys, the old deploy
   key's line from `authorized_keys`, the old signing key from the git host's
   signing keys, and, in the release signers file, the old key's line for the
   principal, replaced by the new key's (as root, written as a new file and
   renamed over the old one; it stays root's and not writable by group or
   others, with every folder above it, or `ah-switch` refuses it). For git:
   - **Current trust:** revoke the old signing key. List its public key in a
     revocation file (`git config gpg.ssh.revocationFile FILE`, a file of
     public keys or an `ssh-keygen -k` key revocation list) and take it out
     of the allowed-signers file: git then refuses every signature it made,
     whatever date the signature claims.
   - **Archival verification:** before you revoke it, record the object ids
     of the commits and tags it signed that you keep (at least each release
     tag and its commit, `git rev-parse TAG TAG^{commit}`), on a machine
     agents do not use. Later, history is checked against that record: an
     object id fixes the content and all the history below it.
   - Do not rely on `valid-before` in the allowed-signers file: git compares
     it with the signing time recorded in the object, which whoever holds the
     key writes, so it cannot tell when a signature was really made.

   Then delete the old private keys where they lived: on the development
   host no file of `~/.ssh` (or elsewhere) holds a private key for the git
   host, the authority host or signing.
5. **Check what remains usable once the laptop's keys are loaded.** In a
   forwarded session, `ssh-add -l` lists only the laptop's keys, each with its
   laptop fingerprint. Without the agent (`env -u SSH_AUTH_SOCK`), a push
   fails with `Permission denied (publickey)`, and so does the negative check
   of the deploy manual; an old key, tried by name
   (`ssh -o IdentitiesOnly=yes -o IdentityAgent=none -i OLD-KEY git@GIT-HOST`
   from wherever a copy may remain), is refused by the git host and by the
   authority host. For signatures, check the old key's rejection and the new
   key's acceptance in each verifier:
   - **The release verifier**, from your root session on the authority host,
     with the command `ah-switch` runs:

     ```
     ssh-keygen -Y verify -f /etc/authority-host/allowed_signers -I PRINCIPAL -n authority-host-release -s SHA256SUMS.sig < SHA256SUMS
     ```

     The sums and signature of the last release the old key signed must now
     fail (`Could not verify signature`); a file you signed with the new key
     on the laptop
     (`ssh-keygen -Y sign -f SIGNING-KEY -n authority-host-release FILE`,
     then the same command with `-s FILE.sig < FILE`) must pass with
     `Good "authority-host-release" signature for PRINCIPAL`.
   - **Git:** a commit or tag signed with the old key does not verify
     (`git verify-commit`, `git verify-tag`), whatever its recorded date, and
     a new tag signed with the new key does.

   What remains usable is the laptop's keys, through the forwarded agent,
   within their `-h` limits, one confirmation at a time.

## Branch and tag protection

Branch and tag protection on the git host is the owner's setting, not this
repository's: no force push and no deletion of `master` and `develop`, and no
moving or deleting a release tag. It bounds what a confirmed push can change.
Nothing here sets it or checks it.
