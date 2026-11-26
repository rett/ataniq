"""Independent schema-1 historical hold-review vector, using Python and OpenSSH."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import tempfile

DATA = Path(__file__).resolve().parent
FORMAT = "ataniq.assignment.schema-1.v2"
NAMESPACE = "ataniq.assignment.event.v1"
KEY = dict(home="fixture-home", project="fixture-project", role="lead")
HOME = dict(authority=KEY["home"], incarnation="first", store="home-store")
EMPTY = dict(authority="", conversation="", id="", incarnation="", kind="",
             launch_intent="", role_epoch="0", store="")
OLD = dict(EMPTY, authority="fixture-a", conversation="a", id="old",
           incarnation="one", kind="legacy", store="store-a")
NEW = dict(EMPTY, authority="fixture-b", conversation="b", id="new",
           incarnation="two", kind="managed", store="store-b")


def canonical(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":")).encode()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def commitment(name, body):
    return sha(("ataniq.assignment." + name + ".v1\0").encode() + body)


def field(value):
    return struct.pack(">I", len(value)) + value


def fixture_key(path):
    """Wrap the public test seed as an unencrypted OpenSSH private key."""
    public = base64.b64decode((DATA / "allowed_signers").read_text().split()[2])
    seed = bytes.fromhex((DATA / "test-key.seed").read_text())
    key_type = field(b"ssh-ed25519")
    raw = public[len(key_type) + 4:]
    assert public == key_type + field(raw) and len(raw) == len(seed) == 32
    private = bytes(8) + public + field(seed + raw) + field(b"fixture")
    private += bytes(range(1, 1 + (-len(private) % 8)))
    container = (b"openssh-key-v1\0" + field(b"none") + field(b"none") + field(b"")
                 + struct.pack(">I", 1) + field(public) + field(private))
    path.write_bytes(b"-----BEGIN OPENSSH PRIVATE KEY-----\n"
                     + base64.encodebytes(container) + b"-----END OPENSSH PRIVATE KEY-----\n")
    path.chmod(0o600)
    return "SHA256:" + base64.b64encode(hashlib.sha256(public).digest()).decode().rstrip("=")


def project_binding(binding):
    return {k: binding[k] for k in ("id", "kind")}


def derive(path):
    key_id = fixture_key(path)
    header = dict(HOME, key_id=key_id, ledger_id="fixture-ledger", schema=1)
    journal = sha(canonical(dict(format=FORMAT, header=header)) + b"\n")
    head = dict(key=KEY, binding_id="", generation="0", phase="", revision="0", transfer_id="")
    holder, cursor, inventory = EMPTY, "", ""
    commit_revision = "0"
    # Request, operation transfer, resulting phase, operation disposition.
    steps = [
        ("Seed", "seed", "committed-closed", "pending"),
        ("ConfirmReceipt", "seed", "committed-closed", "pending"),
        ("Open", "seed", "confirmed", "confirmed"),
        ("Freeze", "move", "frozen", "pending"),
        ("Inventory", "move", "inventoried", "pending"),
        ("BeliefAccepted", "move", "belief-verified", "pending"),
        ("Reserve", "move", "reserved", "pending"),
        ("FenceReceipt", "move", "partially-fenced", "pending"),
        ("Fenced", "move", "fenced", "pending"),
        ("Commit", "move", "committed-closed", "committed"),
        ("ConfirmReceipt", "move", "committed-closed", "committed"),
        ("Open", "move", "confirmed", "confirmed"),
        ("HoldStarted", "move", "confirmed", "confirmed"),
        ("RollbackFreeze", "reverse", "frozen", "pending"),
        ("Inventory", "reverse", "inventoried", "pending"),
        ("HoldReviewed", "move", "inventoried", "confirmed"),
    ]
    scopes = {}
    for rev, (kind, transfer, phase, disposition) in enumerate(steps, 1):
        request = f"{kind}-{rev - 1}"
        c = dict(actor="fixture-owner", authorization="fixture-decision", candidate=EMPTY,
                 earliest_retirement="0", evidence=[], expected=head, hold_start="0", kind=kind,
                 request_id=request, transfer_id=transfer)
        scope, scope_id = head, ""

        def proof(name, subject, outcome, digest=None):
            digest = digest or sha("\0".join((name, subject, outcome)).encode())
            c["evidence"].append(dict(digest=digest, issuer="fixture-source", kind=name,
                                      outcome=outcome, revision="source-rev-A", route="",
                                      scope=scope, scope_event_id=scope_id, subject=subject,
                                      transfer_id=transfer))
            return digest

        old_holder = holder
        if kind == "Seed":
            c["candidate"] = holder = OLD
            proof("enrollment", "old", "verified")
            proof("route", "qargi", "enabled")
        elif kind in ("Freeze", "RollbackFreeze"):
            c["candidate"] = NEW if kind == "Freeze" else OLD
            proof("barrier", holder["id"], "captured")
            proof("route", "qargi", "enabled")
            inventory = ""
        elif kind == "Inventory":
            custom = sha(b"reverse inventory") if transfer == "reverse" else None
            inventory = proof("inventory", KEY["project"], "complete", custom)
            proof("handoff", KEY["project"], "verified")
            proof("route", "qargi", "frozen")
        elif kind == "BeliefAccepted":
            proof("belief", inventory, "matched")
        elif kind == "Reserve":
            c["candidate"] = NEW
            decision = proof("decision", "new", "authorized")
        elif kind == "FenceReceipt":
            scope, scope_id = scopes["Reserve"]
            proof("route", "qargi", "sealed-and-drained")
        elif kind == "Fenced":
            proof("revalidation", inventory, "current")
        elif kind == "Commit":
            c["candidate"] = holder = NEW
            proof("revalidation", decision, "current")
        elif kind == "ConfirmReceipt":
            scope, scope_id = scopes["Commit" if transfer == "move" else "Seed"]
            proof("route", "qargi", "installed-stale-refused")
        elif kind == "Open":
            proof("enrollment", holder["id"], "current")
        elif kind == "HoldStarted":
            c["hold_start"], c["earliest_retirement"] = "10", str(10 + 14*24*3600)
            hold = proof("fallback", "old", "stopped-disabled-preserved")
        elif kind == "HoldReviewed":
            proof("fallback", hold, "reviewed")

        before = head
        if kind in ("Seed", "Commit"):
            commit_revision = str(rev)
        active = "" if phase == "confirmed" else transfer
        if kind == "HoldReviewed":
            active = "reverse"
        head = dict(key=KEY, binding_id=holder["id"], generation="1" if rev < 10 else "2",
                    revision=str(rev), phase=phase, transfer_id=active)
        result = dict(disposition=disposition, event_id="", head=head, request_id=request)
        request_digest = commitment("request", canonical(c))
        record = dict(previous_hash=journal, request_digest=request_digest, result=result,
                      schema=1, sequence=str(rev))
        event_id = commitment("public", canonical(record))
        result["event_id"] = event_id
        scopes[kind] = head, event_id
        public = dict(active_transfer_id=active, admission=phase == "confirmed", alg="Ed25519",
                      binding=project_binding(holder), commit_revision=commit_revision, event_id=event_id,
                      evidence_digests=[p["digest"] for p in c["evidence"]],
                      executing_instance={k: holder[k] for k in HOME}, format=FORMAT,
                      generation=head["generation"], home=HOME, inventory_digest=inventory, key_id=key_id,
                      kind=kind, old_binding=project_binding(old_holder), phase=phase,
                      previous_generation=before["generation"], previous_public_digest=cursor,
                      previous_revision=before["revision"], project_id=KEY["project"], request_id=request,
                      revision=str(rev), role_id=KEY["role"], schema=1, transfer_id=transfer)
        body = canonical(public)
        detached = subprocess.run(["ssh-keygen", "-Y", "sign", "-f", path, "-n", NAMESPACE],
                                  input=NAMESPACE.encode() + b"\0" + body, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, check=True).stdout
        blob = base64.b64decode(b"".join(detached.splitlines()[1:-1]))
        public["signature"] = base64.urlsafe_b64encode(blob).decode().rstrip("=")
        cursor = sha(canonical(public))
        journal = commitment("ledger", canonical(dict(record, command=c, public=public)) + b"\n")
        if rev == 13:
            print("hold checkpoint", journal)
    print("historical review cursor", cursor)
    return body, detached


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="replace the independent pair after a format change")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory() as tmp:
        body, detached = derive(Path(tmp) / "test-key")
    for suffix, data in (("json", body), ("sig", detached)):
        dest = DATA / ("historical-review." + suffix)
        if args.write:
            dest.write_bytes(data)
        elif data != dest.read_bytes():
            raise SystemExit(f"{dest.name} differs from independent derivation")
