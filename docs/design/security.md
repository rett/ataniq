# Execution security contract

Status: requirements for qualification, not a claim of isolation. Linux,
systemd and cgroup v2 are the initial execution target. A user service and its
cgroup provide lifecycle and resource controls; same-user execution alone does
not isolate hostile agent code from the authority, runner or credentials.
Provider execution is supported only after its complete boundary is qualified.

## Trust boundaries

The host authority authenticates each enrolled owner client and authorizes
each action against project policy. Provider credentials, client credentials,
runner control credentials and federation credentials have separate scopes and
lifecycles. Never expose control-plane credentials to agent-executed code or
forward an Ataniq client token to a provider or peer. Use authenticated local
runner IPC with epoch binding; socket location or same-user ownership alone is
not an authorization contract. Secrets are references resolved within the
appropriate boundary, excluded from public artifacts, logs and previews.

Qualify the provider, version, launch configuration and execution boundary as
one combination. Demonstrate that agent code cannot read or impersonate the
authority or runner, access other projects' data or credentials, escape its
permitted workspace or network policy, or bypass the limits claimed for it.
Define filesystem, network, process, identity, syscall and credential access
policies and their host prerequisites. A failed, absent or stale qualification
blocks execution and is visible as unsupported; a health check or systemd unit
start does not substitute for adversarial tests. Capability changes or changed
versions require requalification. Native direct access is unsupported if it
bypasses required client authorization, audit, budget or approval controls.

An agent on a provider's cloud in a supervisory role is outside every boundary
the owner's hosts qualify. It reaches an instance only through that instance's
scoped supervisor port, with a port credential of its own kind that the owner
enrols and can revoke, valid for that instance and that port only. Its client
class carries no approval and no grant, and it is never given a control-plane,
owner-client, runner or federation credential. The class is bound by the
instance when the owner enrols the client; a class the client declares is never
honoured. A gateway enforces nothing unless it runs inside a qualified boundary
on the owner's hosts; otherwise the instance enforces. The provider's own
console is a second way to drive its session and is no approval path. The
supervisor port, the port credential, the client class and the gateway are not
built, and nothing here is qualified; until they are, such an agent is
unsupported.

Data goes to a third-party provider only per project, with the owner's consent,
and the instance must record what was sent, by identity and digest. The first
projects are scratch projects, and the data is the digest of daemon-written
state plus the transcripts of leads. Free text a lead wrote is untrusted, in a
transcript and in a request. The session that reads transcripts is never the
session that acts. The instance enforces this by client identity, not by a
prompt, and it is not asked of the agent in a prompt: it serves a transcript
only to a client whose one write call is returning its summary under a schema,
and accepts a nomination, a proposal or any other write call only from a client
it has never served a transcript. A returned summary changes no record but the
summary itself. A request's free-text fields are served to no client that can
make a write call; such a client is served typed fields only. The reading
session returns its summary to the instance under a schema; the instance checks
it and serves its free text, which stays untrusted, to the owner only. A client
that can make a write call is served at most the summary's typed fields. A
summary across instances is made for the owner outside every instance and is
served to no instance; how it reaches the owner is decided with the supervisor
port. What a provider does between two of its own sessions is outside the
instance's control and is not claimed.

## Containment and termination

The execution boundary must identify the full process tree and apply the
declared cgroup limits and termination policy. Verify descendants cannot escape
the boundary under the configured threat model. A process group signal or
direct child kill alone does not establish tree termination. Confirm the
boundary is empty and reconcile provider and remote children before declaring
termination; when proof is unavailable, show unknown. A separate bounded child
boundary is needed where independent cancellation, credentials or limits are
required; a native child may lack that capability and must be reported as such.
Limits specify whether they are admission-time, actively enforced, estimated
or advisory. Do not claim a hard token or cost ceiling for an opaque call that
cannot enforce it. On outage, retain already dispatched work within its
boundary and pause new dispatch and approvals. Qualification must enforce the
provider/tool dispatch boundary and outage rules in [runtime](runtime.md),
including child attempts and retries; an opaque, unenforceable boundary is
unsupported.

## Artifacts and previews

Artifacts carry source, digest, project, producer attempt and access policy.
Render static text, images and diffs through safe preview handling by default;
downloads require authorization. Treat all content as untrusted. Active HTML
or application previews require an explicit action and isolated origin and
sandbox with no ambient control credentials, restricted network and storage,
short lifetime and explicit capability grants. Recheck artifact permission on
access and close or revoke preview access when policy changes. A preview is not
an approval or proof that an artifact is safe.

## Acceptance scenarios

With a disposable host and fake credentials, attempt credential reads, control
IPC impersonation, cross-project filesystem access, network policy escape and
descendant escape. Each prohibited operation must fail and produce auditable
evidence; any untested or successful escape makes the combination unsupported.
Spawn a descendant that creates a new session, cancel the attempt and assert
the qualified boundary is empty before reporting stopped. Simulate an
unreachable remote child and assert unknown, not stopped. Open active content
that attempts to read console credentials or contact a forbidden endpoint and
assert denial. Revoke artifact access and assert subsequent preview and download
requests fail. Have a supervisory agent on a provider present an approval, a
grant, a nomination that carries text or fields of its own, or a nomination of
an unknown, changed or already decided request or of another instance's record,
and assert refusal; send it a call shaped like a grant and assert refusal; and
assert a session that read lead transcripts can make no call but returning its
summary. These scenarios require a real qualified boundary and have not been
executed.
