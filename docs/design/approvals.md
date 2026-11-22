# Approvals and delegation

Status: owner-approved direction, specification only for grants; no grant
format, signer or gate verification is implemented or qualified. The host's
side of the owner channel is built only in a daemon made with the build tag
`ataniq_ownerchannel`, over a stand-in door and scratch state, and none of it
is qualified (see
[the lead daemon guide](../lead-daemon.md#the-owner-channel-tagged-build-only)).
The link that would carry the owner's decision from a paired phone, a passkey
assertion over a challenge the daemon binds to its own record, is specified in
[the seat gate](seat-gate.md) and is not built; until it and the owner channel
are qualified, it decides nothing real. Today approvals are the owner's direct
words. The formats, key handling and storage of grants are still to be designed
and reviewed. A paragraph that begins "Proposed, not yet the
owner's decision" is a proposal for the owner to decide, not part of the
owner-approved direction.

## Who can approve

Only two things approve anything:

- The owner's direct words, given in the owner's own session or at the owner
  channel (see [owner channel](#owner-channel) below).
- A delegation-chain grant whose signatures verify back to the owner's root
  key.

Nothing else approves: not a lead's judgement, not a supervisor's say-so, not
a connection to a conversation, a plan or preview, terminal output, or a
provider ceremony that bypasses arbitration. Direction relayed by another
agent or a cloud assistant is never an approval, however faithfully it repeats
the owner.

An agent that runs on a provider's cloud, or anywhere else outside an execution
boundary that Ataniq qualifies on the owner's hosts, holds, sees and passes no
approval token, plan token or delegation grant, whatever role it holds. It may
read that a request was decided, as status. A supervisor that runs on a
provider's cloud can nominate a request, which reaches the owner only as a card
(see [nominations](#nominations-from-a-remote-supervisory-agent) below); any
other holder outside a qualified boundary is unsupported as a remote
supervisory agent.

A feature coordinator, the ephemeral delegate of a lead for one feature (see
[roles and authority](roles-and-authority.md)), never approves anything. No
approval token, plan token or grant passes to or through a coordinator. A lead's
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
lead that runs on a provider's cloud, and not a relay such as a gateway.

Proposed, not yet the owner's decision: no grantee holds a signing key. A
sub-grant would be issued by an issuer inside a qualified boundary on the
owner's hosts, which checks that it narrows its parent and records it in the
one store below, never by the role that receives or relays it. The issuer, its
key and its place in the chain are still to be designed.

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
approver. Each instance assigns it as its own remote supervisory client and
fences it alone. It reads status and requests from the leads of its assigned
instances, and from a local supervisor of an instance where one is assigned,
each through that instance's scoped port, and may summarise across the
instances; that summary is made for the owner outside every instance and is
served to no instance. It nominates requests. A nomination or a proposal to an
instance names only that instance's records. A nomination is a reference to a
request, not a message to a lead and not an approval:

- A nomination names a request that already exists in the instance, an
  outstanding approval of [client protocol](client-protocol.md) made there by a
  lead of that instance, by its identity and revision. The agent supplies no
  field of what is approved, and no text of its own appears on the card in the
  first version. The instance renders the card from its own record, and the card
  is bound to that outstanding approval's immutable approval record. A
  nomination of an unknown, changed or already decided request is refused. If the request changes, the card is stale
  and decides nothing.
- The owner decides at the owner channel (below), and the decision returns
  straight to the instance. It does not pass through the nominating agent or any
  relay, and the agent holds no approval token that stands for it.
- The card says it came from a remote supervisory agent. It shows the typed
  fields first: the project, the action, the exact target and revision, who
  requested it inside the instance, what happens on approve, the expiry, and the
  choices approve, refuse or leave. Any text a lead wrote that the card shows is
  marked untrusted. The fields a card must show, at least these, are settled
  with the supervisor port.
- A card the owner does not decide never approves by default. It expires
  undecided and the request stays as it was.
- One card carries one request and one decision. No decision covers a card the
  owner has not opened, and a nomination does not change a request's priority or
  order. The instance limits the nominations it accepts from one agent and shows
  a request nominated twice as one card. The limits are decided with the
  supervisor port.

Which kinds of request may be nominated is decided with the supervisor port.
A request to start or stop a lead is not among the first kinds: such cards from
a cloud agent come later, with a daily limit the instance enforces, and marked
on the card as a start or stop asked by a remote agent. This is the one case
where the agent is the originator of the request; its fields are then a closed
set (which lead, and start or stop), with no free text.

The agent makes proposals only. It never edits a plan, a backlog or a
dependency manifest. A proposal against an expected revision changes nothing
until the project's lead or the owner accepts it through the system that owns
the record (see [roles and authority](roles-and-authority.md)).

The supervisor port, the port credential of such an agent (see
[security](security.md)), the client class that identifies it and the gateway
that carries its calls are not built, and nothing here is qualified. The class
is bound by the instance when the owner enrols the client; a class the client
declares is never honoured. A gateway enforces nothing unless it runs inside a
qualified boundary on the owner's hosts; otherwise the instance enforces. No
card from a remote supervisory agent reaches the owner before the owner channel
(below) is qualified. Until these exist no remote supervisory agent is
supported, and approvals remain the owner's direct words. What an agent on a
third party's provider may read is in [security](security.md).

## Messages are not authority

A message is never the authority. A signed grant may travel by message,
because the gate verifies the signatures, not the carrier. The carrier is still
never a role whose holder runs outside a qualified boundary (see Grants): the
supervisor port has no call that carries a grant, and anything shaped like one
is refused there. A message that claims an approval without a verifying grant
has no effect.

## Owner channel

The owner approves through a channel that leads cannot reach: an endpoint that
the lead's sandbox never mounts. The owner's root key is only used in the
owner's own terminal. Until that channel and the sandbox that keeps leads off
it are qualified, no grant is honoured and approvals remain the owner's direct
words.
