# Canary intake template

`canary-intake.json` is the sandbox template that `ataniq-intake` uses for the
canary slice's throwaway repository: the `serve` half (the fetch from the
lead's clone and the size of its object store) and the `verify` half (the
gates). The format is in [the sandbox template](../../../docs/lead-intake.md#the-sandbox-template).
Nothing in the intake is changed for it.

When you approve it, you vouch for these. Each item is one line.

- **Read-only binds:** `/usr` of the host, with `/bin`, `/lib` and `/lib64` as links into it; in `serve` the lead's clone (`{tree}`), in `verify` the mirror (`{mirror}`).
- **Writable binds:** none in `serve`; in `verify` the scratch folder and the home folder of the run (`{scratch}`, `{home}`, empty, removed by the intake afterwards) and a `/tmp` in memory.
- **Not bound:** the caller's home, `/etc`, `/var`, `/run`, `/sys`, the rest of the state root and every other folder; the environment is cleared and only `PATH` (and in `verify` `HOME`, `TMPDIR`) is set.
- **Shared with the host:** the kernel itself, and the caller's kernel keyrings: the session keyring of the process that starts the sandbox and whatever it links, such as the user keyring; nothing else. It unshares the network (a namespace of its own with only a loopback interface, so no route to the host or to any network, and none of the host's abstract Unix sockets), PID, IPC, UTS (with a fixed host name), cgroup and user namespaces; the user namespace maps the caller's own user and group id to themselves, drops every capability and refuses a further user namespace; `--die-with-parent` and `--new-session` are set.
- **Programs:** `/usr/bin/bwrap` (the launcher, which runs as the owner); in `serve` `/usr/bin/git-upload-pack` and the git helpers under `/usr`, and `/usr/bin/du` to measure; in `verify` `/bin/sh` and `git` from the intake's own script, then the gates you pass with `--gate`.
- **Not confined:** no seccomp filter, so the kernel's whole system call surface is open to the code inside, `keyctl` included: run the intake from a session whose keyring holds no key, for example under `keyctl session -`; in `verify`, `/proc` shows the host paths of the binds (in `/proc/self/mountinfo`) and the kernel's global files, so choose a `--work` folder whose path names nothing private; no limit on CPU, memory, processes or the disk used by the scratch and home folders (only the intake's time and output limits); all of `/usr` is readable, including anything a host keeps in `/usr/local`; bubblewrap resolves the bind sources as the owner, so the caller must check the path to the clone and the verify folders; the template does not check that this host has `/usr/bin/bwrap` (version 0.8 or newer, for `--disable-userns`), a `/usr` layout with `bin`, `lib` and `lib64` links, or user namespaces: if it does not, a run fails instead of running unconfined.
- **The test:** `go test ./qualification/canary/...` checks form only: that the intake's own parser accepts the file, what the argument lists it makes contain, and that the file and this note hold no home path, host name or user name. It starts no namespace and runs no bubblewrap, so it does not show that anything confines.

`serve.repository` is a path inside the sandbox and must not exist on the host
(`/ataniq` here). The fetch gives it to the owner's git as a `file://` URL, so
git never reads a file at that path as a bundle, and the fetch refuses a path
that exists. Check it before the `qualify-mirror` run and the canary's
`ataniq-intake fetch`, so the refusal does not surprise you.

What is specific to a machine is not in the file. You pass it when you run the
intake: `--sandbox` (the path of the template, outside the state root),
`--state`, `--project`, `--work` and, for the canary's check, the gate that
looks at `canary-token.txt` in the tree's top folder (`--gate NAME=COMMAND`).
The paths in the template are the paths inside the sandbox.
