"""Print the fixed checkpoint transcript using Python's standard library and OpenSSH."""
import base64
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import tempfile


PUBLIC = {
    "witness": "988a2ffe01eea7328e69c8cd580eeb04340f7cb80d8757ca28843ae3d8aa4e0a",
    "home": "18e1e27a83c80015a9dadfa0a2b108df41de4872c319a3fa7eb91302a116874f",
    "session": "0c801e5cb5115a544df660d10446bcbdabbbce1699037a48b3099bddf581e1f2",
}


def field(b):
    return struct.pack(">I", len(b)) + b


def keyfile(directory, role):
    raw = bytes.fromhex(PUBLIC[role])
    seed = hashlib.sha256(("checkpoint fixture " + role).encode()).digest()
    blob = field(b"ssh-ed25519") + field(raw)
    private = bytes(8) + blob + field(seed + raw) + field(b"fixture")
    private += bytes(range(1, 1 + (-len(private) % 8)))
    container = (b"openssh-key-v1\0" + field(b"none") * 2 + field(b"")
                 + struct.pack(">I", 1) + field(blob) + field(private))
    path = directory / role
    path.write_bytes(b"-----BEGIN OPENSSH PRIVATE KEY-----\n" + base64.encodebytes(container)
                     + b"-----END OPENSSH PRIVATE KEY-----\n")
    path.chmod(0o600)
    return "SHA256:" + base64.b64encode(hashlib.sha256(blob).digest()).decode().rstrip("=")


def canon(value):
    return json.dumps(value, separators=(",", ":"), sort_keys=True).encode()


def enc(value):
    return base64.urlsafe_b64encode(value).decode().rstrip("=")


def sha(value):
    return hashlib.sha256(value).hexdigest()


def derive(directory):
    ids = {role: keyfile(directory, role) for role in PUBLIC}

    def record(name, body, roles=()):
        sigs = {}
        for role in roles:
            ns = "ataniq.checkpoint." + role + ".v1"
            armor = subprocess.run(
                ["ssh-keygen", "-Y", "sign", "-f", directory / role, "-n", ns, "-O", "hashalg=sha512"],
                input=ns.encode() + b"\0" + canon(body), capture_output=True, check=True).stdout
            sigs[role] = enc(base64.b64decode(b"".join(armor.splitlines()[1:-1]), validate=True))
        print(canon(dict(name=name,body=body,signatures=sigs)).decode())
        return sigs

    slot = dict(witness="witness", authority="home", store="store")
    header = dict(authority="home", store="store", incarnation="first", schema=1,
                  ledger_id="ledger", key_id=ids["home"])
    raw = canon(dict(format="ataniq.assignment.schema-1.v2", header=header)) + b"\n"
    head = dict(authority="home", store="store", ledger_id="ledger", incarnation="first",
                sequence="0", digest=sha(raw))
    state = dict(slot=slot, header=header, head=head, epoch="1", revision="1", fence="0",
                 home_key_id=ids["home"], session_key_id="", status="active", last_receipt_digest="")

    def base(op, role, audience):
        return dict(schema=1, operation=op, key_id=ids[role], audience=audience)

    def ack(op, request):
        state["journal_sequence"] = str(1 + int(state["revision"]))
        body = dict(base(op, "witness", "home"), id=request["id"], result=state,
                    request_digest=sha(b"ataniq.checkpoint.request.v1\0" + canon(request)))
        sig = record(op + "-ack", body, ["witness"])["witness"]
        state["last_receipt_digest"] = sha(b"ataniq.checkpoint.receipt.v1\0" + canon(body)
                                           + b"\0" + sig.encode())

    request = dict(base("enroll", "home", "witness"), slot=slot, id=sha(b"enroll"),
                   header_bytes=enc(raw), home_public_key=enc(bytes.fromhex(PUBLIC["home"])))
    record("enroll", request, ["home"])
    ack("enroll", request)

    for revision, op in enumerate(("claim", "advance", "release", "retire"), start=2):
        role = "home" if op in ("claim", "advance") else "witness"
        request = dict(base(op, role, "witness"), id=sha(op.encode()), expected=dict(state))
        if op == "claim":
            request["session_public_key"] = enc(bytes.fromhex(PUBLIC["session"]))
            state.update(session_key_id=ids["session"], fence="1")
        elif op == "advance":
            request["next"] = dict(head, sequence="1", digest=sha(b"next frame"))
            state["head"] = request["next"]
        elif op == "release":
            state.update(session_key_id="", fence="2")
        else:
            request["disposition_digest"] = sha(b"disposition")
            state.update(fence="3", status="retired")
        record(op, request, ["home", "session"] if role == "home" else [])
        state["revision"] = str(revision)
        ack(op, request)
        if op == "advance":
            body = dict(base("read", "witness", "home"), challenge=sha(b"challenge"), guard="ready",
                        current=state, receipt_id=request["id"], prior_digest=state["last_receipt_digest"])
            record("read", body, ["witness"])


if __name__ == "__main__":
    with tempfile.TemporaryDirectory() as folder:
        derive(Path(folder))
