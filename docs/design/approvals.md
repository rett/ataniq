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

An agent that runs on a provider's cloud, or anywhere else outside an execution
boundary that Ataniq qualifies on the owner's hosts, holds, sees and passes no
approval, plan token or delegation grant, whatever role it holds. What such an
agent can do is nominate a request, which reaches the owner only as a card (see
[nominations](#nominations-from-a-remote-supervisory-agent) below).

A feature coordinator, the ephemeral delegate of a lead for one feature (see
[roles and authority](roles-and-authority.md)), never approves anything. No
approval, plan token or grant passes to or through a coordinator. A lead's
feature delegation to a coordinator is neither a grant nor an approval and is
not part of the delegation chain below: it hands over some of the lead's own
verbs on one feature and carries no owner gate. Its questions go to its lead,
and the lead relays them to the owner. The owner typing into a coordinator's
session is not an approval through it: the coordinator still cannot finish,
push or approve.

## Grants

A delegation chain starts with the owner's root grant to a grantee the owner
names. A grantee is a role whose holder runs inside an execution boundary that
Ataniq qualifies on the owner's hosts. No grant is issued to, held by or passed
through a role whose holder runs outside such a boundary: not a supervisor or a
lead that runs on a provider's cloud, and not a relay such as a gateway. No
grantee holds a signing key: a sub-grant is issued by code on the authority
host, which checks that it narrows its parent and journals it, never by the
role that receives or relays it. The issuer is still to be designed.

Whether a locally held supervisor is a grantee at all is open, and it is the
owner's decision. Until the owner decides, no supervisor is a grantee and the
interim arrangement in [roles and authority](roles-and-authority.md) carries no
grant. If the owner makes it one, its root grant may let part of its scope pass
on as narrower sub-grants to individual leads whose holders also run inside a
qualified boundary. Every grant has:

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

## Nominations from a remote supervisory agent

The supervisor role may be held by an agent on any provider, local or cloud
(see [roles and authority](roles-and-authority.md)). A supervisor that runs on a
provider's cloud is an owner-side facilitator, not a grantee and not an
approver. It reads status and requests from the supervisors and leads of its
assigned instances, each through that instance's scoped port, summarises across
the instances, and nominates requests. A nomination is a typed request, not a
message to a lead and not an approval:

- The instance renders the card from the structured request, so the nominating
  agent cannot reword what the owner approves. Any text the agent adds is shown
  apart, marked as the agent's, and is not part of what the owner decides.
- The owner decides at the owner channel (below), and the decision returns
  straight to the instance. It does not pass through the nominating agent or any
  relay, and the agent holds no token that stands for it.
- The card says it came from a remote supervisory agent.
- A card the owner does not decide never approves by default.

Which kinds of request may be nominated is decided with the supervisor port.
A request to start or stop a lead is not among the first kinds: such cards from
a cloud agent come later, with a daily limit per nominating agent, marked on the
card, and not before the owner channel is qualified.

The agent makes proposals only. It never edits a plan, a backlog or a
dependency manifest. A proposal against an expected revision changes nothing
until the project's lead or the owner accepts it through the system that owns
the record (see [roles and authority](roles-and-authority.md)).

The supervisor port, the client class that identifies such an agent (a field
of client identity, not enforced) and the gateway that carries its calls are
not built, and nothing here is qualified. Until they exist no remote
supervisory agent is supported, and approvals remain the owner's direct words.
What an agent on a third party's provider may read is in
[security](security.md).

## Messages are not authority

A message is never the authority. A signed grant may travel by message,
because the gate verifies the signatures, not the carrier. The carrier is still
never a role whose holder runs outside a qualified boundary (see Grants). A
message that claims an approval without a verifying grant has no effect.

## Owner channel

The owner approves through a channel that leads cannot reach: an endpoint that
the lead's sandbox never mounts. The owner's root key is only used in the
owner's own terminal. Until that channel and the sandbox that keeps leads off
it are qualified, no grant is honoured and approvals remain the owner's direct
words.
