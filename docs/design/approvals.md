# Approvals and delegation

Status: proposed, owner-approved direction; no grant format, signer, owner
channel or gate verification is implemented or qualified. Today approvals are
the owner's direct words. The formats, key handling and storage are still to be
designed and reviewed.

## Who can approve

Only two things approve anything:

- The owner's direct words, given in the owner's own session.
- A delegation-chain grant whose signatures verify back to the owner's root
  key.

Nothing else approves: not a lead's judgement, not a supervisor's say-so, not
a connection to a conversation, a plan or preview, terminal output, or a
provider ceremony that bypasses arbitration. Direction relayed by another
agent or a cloud assistant is never an approval, however faithfully it repeats
the owner.

A coordinator, the ephemeral delegate of a lead for one feature (see
[roles and authority](roles-and-authority.md)), never approves anything. No
approval, plan token or grant passes to or through a coordinator, and a
delegation to a coordinator is not an approval: it hands over some of the
lead's own verbs on one feature and carries no owner gate. Its questions go to
its lead, and the lead relays them to the owner.

## Grants

A delegation chain starts with the owner's root grant to the supervisor, which
may pass part of its scope on as narrower sub-grants to individual leads. Every
grant has:

- scope: the projects, actions and, for a sub-grant, the one lead and one
  project it is issued to; a sub-grant can never exceed its parent;
- expiry;
- a use count, with a sub-grant single-use;
- revocation, which the gate checks;
- audit: each use writes an audit entry, and a use whose audit cannot be
  written is refused.

A grant never covers the grantee's own project, so no role approves its own
work. The gate, not the receiving lead's own judgement, verifies the chain,
the identity it was issued to, expiry, revocation and replay, and records a
single-use grant as used before the command acts.

Delegation grants are kept in one store, in qargi's signed and audited
records, and the first version honours them for qargi gates only. The word
"grant" has other meanings in these documents; they are distinct from the
delegation grant here: the one-use decision grant consumed with command state
in [client protocol](client-protocol.md), the exact-target grant in dispatch
in [runtime](runtime.md), and directional federation grants in
[work model](work-model.md).

## Messages are not authority

A message is never the authority. A signed grant may travel by message,
because the gate verifies the signatures, not the carrier. A message that
claims an approval without a verifying grant has no effect.

## Owner channel

The owner approves through a channel that leads cannot reach: an endpoint that
the lead's sandbox never mounts. The owner's root key is only used in the
owner's own terminal. Until that channel and the sandbox that keeps leads off
it are qualified, no grant is honoured and approvals remain the owner's direct
words.
