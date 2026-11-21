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
scoped supervisor port, as a client of a class that carries no approval, grant
or credential, and it is never given a control-plane, client, runner or
federation credential. The provider's own console is a second way to drive its
session and is no approval path. The supervisor port, the client class and the
gateway are not built; until they are, such an agent is unsupported.

Data goes to a third-party provider only per project, with the owner's consent,
and the instance records what was sent. The first projects are scratch
projects, and the data is the digest of daemon-written state plus the
transcripts of leads. Free text a lead wrote is untrusted. The session that
reads transcripts is never the session that acts: it returns a summary under a
schema, and a session that nominates, proposes or messages never receives the
raw text. This split is enforced in the instance or its gateway, not asked of
the agent in a prompt.

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
grant or a reworded card and assert refusal, and assert a session that read lead
transcripts cannot make a write call. These scenarios require a real qualified
boundary and have not been executed.
