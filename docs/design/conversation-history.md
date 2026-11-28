# Conversation and terminal history

CF04 proposes two reads for a scratch lead. Use **Conversation** to find an
instruction, its receipt, or the lead's written reply. Use **Recent output** to
see what scrolled off the holder's terminal, including while the laptop was
disconnected. The laptop desk can read and attach as a viewer. Decisions stay
on the seat through its existing card and decision path. Both history views
are scratch-only until the owner's signed go for managed use.

## What an entry means

| View and provenance label | Source and meaning |
| --- | --- |
| Conversation · Lead reply | Text the lead published to its reply outbox. The daemon assigns the sender and record time. Both clients render it as untrusted text. |
| Conversation · Instruction | Show who issued the inject or typed command and link to the original record. Include its text only when the source retained it and the reader may see it. Otherwise show the operation alone. |
| Conversation · Receipt / status | The authority's admission evidence and the separately revisioned status. Label a scratch inject result as the holder's reported outcome. A lost acknowledgement leaves delivery unknown pending reconciliation. |
| Conversation · System | A store or policy observation, including the start of a readable segment or an omission. Only the authority assigns this label. |
| Recent output · Holder | Rendered main-screen rows from this holder epoch. Echoed input, tool output, prompts and text resembling approvals all have the same terminal provenance. None creates a conversation entry or authorizes an action. |

For a known sensitive interval (for example a login or credential prompt),
record **omitted** with no content. Suppress capture before buffering or
serializing either history. Retain one fixed marker for the interval only if
its existence is disclosable. Exclude text, cells, links, thumbnails, digests
of the omitted content and per-row counts. Resume history only from clean
content so that a line left on screen during login cannot later scroll into
the ring. Apply the same rule to history served on reattach. Detecting safe
capture boundaries on a real provider screen is **UNVERIFIED**. Keep capture
off while a boundary is uncertain.

## One conversation page for desk and seat

Address the existing `appcontract.ConversationResource` by its project and
`appmodel.ConversationRef{Authority, ID}`. The resource revision tracks
admission. The authority gives each entry one stable message ID
**{authority, lead, conversation, segment generation, seq}**. Lead identifies
the stable lead role, conversation is the ID from that ref, and seq orders
entries within the segment. It counts the entries the reading viewer's
disclosure projection does not hide, so a hidden entry consumes no number and
the IDs of what a viewer sees run without a gap; the snapshot carries how many
pruned entries were hidden, so retention never renumbers. For a projection that
hides nothing it is the authority's sequence. A fresh readable segment gets a
fresh generation. An attach or a page refresh preserves the ID, and so does a
change of viewer whose projection hides the same entries below it. Desk and seat
deduplicate by the whole tuple. Preserve existing event and cause IDs when an
entry refers to those events. [Resource definition](../../internal/appcontract/resource.go)
and [event identities](../../internal/appcontract/projection.go).

An instruction uses the existing `CommandKey` (authority, enrolled client ID
and command ID). Keep its immutable `AdmissionReceipt` (key, digest,
`AdmittedRevision`, historical cursor) and read its `CurrentStatus` by that
key under current disclosure rights. A status update leaves the receipt and
message ID intact. Reconcile a lost response by the original key. Paging or
viewing an uncertain command must never resend it. [Application contracts](application-contracts.md#admission-and-status-contract-for-later-composition)
and [model types](../../internal/appmodel/model.go).

Start at the newest authorized entries of a consistent snapshot, ordered by
sequence within each page. Return an opaque **backward cursor** when an older
page can be read. It selects entries strictly before the oldest entry already
returned. Bound the page by entry count and bytes. A later append cannot
move the boundary. Use the encrypted, instance-bound v2 codec that scopes
events to the viewer, project and conversation and binds causes to a command
and its acknowledged interval. Extend its closed set of query contexts to bind
the backward read to the viewer, project, conversation, lead, segment, snapshot
and projection generation. The instance binds authority, store ID and
incarnation. A forward token cannot resume a backward read.
Keep the forward replay watermark and the receipt cursor in their original
roles. Clients pass tokens back unchanged. Check current access before decoding
or reporting an error that could disclose history. [Cursor implementation](../../internal/appcontract/cursor.go)
and [replay contract](client-protocol.md#events-replay-and-snapshots).

| Page state | What the reader does |
| --- | --- |
| **end** | The authority can account for the start of the readable segment. Show its beginning and stop paging. An empty new segment can end here. |
| **gap** | A valid read reaches a known loss, such as entries pruned by retention. Mark the break at the oldest retained boundary. Never infer loss or its size from nonconsecutive visible IDs. |
| **reset** | An expired or invalid continuation, changed segment, instance or disclosure projection requires `reset_required` for an authorized reader. Discard the old page and cursor and obtain a fresh authorized snapshot. Show a gap there if retention cut off older entries. |
| **withheld** | Render an authorized omission as a content-free marker. Continue paging past it if a cursor is supplied. When even existence is hidden, suppress the marker and any identifying counts, including a gap in the visible numbers: the reader numbers the visible entries consecutively. |

Proposed retention per conversation is the newest **500 entries** within
**14 days**, whichever limit removes an entry first. Evicting a display entry
does not delete the command's authoritative receipt or reconciliation evidence.
Today `internal/leadchat` is the tagged scratch reply store. It has these two
bounds, pins its latest turn-on entry, and opens off after restart. Its numeric
`after` reads forward. Reuse each scratch reply's recorded generation and seq.
For a mixed scratch page, reserve positions for instructions and receipts from
that same sequence as they are recorded. Keep their source references. Both
surfaces must obtain the coordinates with the entry under the same read guard.
The managed page contract belongs to the application layer. Scratch client
strings and receipts cannot establish managed enrollment or admission.
[Scratch store](../../internal/leadchat/doc.go),
[seat chat wire](../../internal/approval/ownerwire/chat.go) and
[scratch command replay](../scratch-leads.md).

## Recent output across an SSH drop

Today the console opts into 64 local rows and restoring a holder snapshot
clears them. The proposed ring belongs to the holder's `sessionpane.Pane`,
which `internal/leadholder/serve` holds for snapshots and output. The session
constructs that pane. Set the bound there through
`sessionpane.Options.RecentOutput` (currently a boolean opt-in). Keep main-screen
rows in memory for the holder epoch, with a provisional cap of **2000 rows**.
Decide the final cap at the attended rehearsal using heap use during scrolling
and the size and latency of a full reattach. **UNVERIFIED** until that sitting.
Keep alternate-screen scrollback at zero. Before the x/vt patch it held 10,000
unreadable rows outside the main-screen limit and could grow the heap by
hundreds of megabytes. [Holder session](../../internal/leadholder/session/session_linux.go),
[serve](../../internal/leadholder/serve/serve_linux.go),
[local rows](../../internal/terminalconsole/sessionpane/recent.go) and
[x/vt patch 3](../../third_party/patched/x-vt/NOTICE.md).

1. An SSH drop detaches the desk. The holder continues collecting eligible rows
   under its cap. On reattach, validate a fresh viewer ticket for the lead and
   epoch. Capture the bounded rows, their oldest marker and the screen snapshot
   at the same output position.
2. Serve the history as bounded attach data. In `internal/leadattach`, validate
   the epoch, position, row and byte limits, then restore the snapshot, install
   the rows as history and follow live output from that position. Installing
   rows must produce no terminal writes or replies. Discard a partial transfer
   or a mismatched position and request a fresh authorized attach.
3. The console shows a visible **oldest retained row** marker at the boundary
   and indicates when earlier rows were evicted. Distinguish an empty ring
   from an omitted interval. A replacement holder starts with an
   empty ring. A peer that serves only the current screen must say earlier rows
   are unavailable. [Current attach client](../../internal/leadattach/doc.go)
   and [console behavior](../terminal-console.md).

## When access ends

| Event | Laptop console | Seat |
| --- | --- | --- |
| Detach or transport loss | Close the affected view. Drop its rendered cells, recent rows, selection, in-app copy buffers, conversation pages and cursors. On reconnect, read afresh. The lead and holder ring survive a viewer detach. | When the seat detaches or loses its link, close its watch and clear the page, message IDs and continuation. A desk detach leaves an independently authorized seat session alone. |
| Revocation or narrower scope | Stop affected reads and attach delivery. Clear affected pages, receipts, IDs, cursors, screen and history caches. Ignore late responses issued under the previous scope. | Stop affected reads and watches. Clear protected rendered content, cached responses, cursors and pending decision UI for lost decision access. On credential revocation, end the affected sessions. |
| End of access | On chat off, segment replacement, holder end or logout, clear the corresponding conversation or terminal view, including its oldest marker. A new grant starts with a fresh snapshot. | On chat off, segment replacement, logout or session expiry, clear affected chat content and navigation state. Prevent browser back/restore from redisplaying it without a new authorized read. The seat receives no terminal ring. |

These are client-state requirements. The existing scratch gate checks reads
through the seat link and an existing session may learn of a passkey revocation
only on a later operation. Immediate managed revocation across the two surfaces
is **UNVERIFIED**. [Seat gate](seat-gate.md).

## Build slices and socket-free acceptance

| Slice | Scope | Acceptance fixture |
| --- | --- | --- |
| Conversation page contract | `internal/appmodel`, `internal/appcontract` and the page value consuming them | Page twice across a concurrent append with a fake clock and in-memory entries. Assert stable IDs, no duplicate or skipped entry, all four states, unchanged receipt after status advances, and reset for a foreign instance, viewer or direction after authorization. |
| Holder recent output | `internal/leadholder/session` and `internal/terminalconsole/sessionpane` | Feed more rows than the cap to a pane. Check the oldest surviving row, zero alternate-screen history, and an omitted interval whose cells never reappear after a scroll or resize. |
| Holder history serve | `internal/leadholder/serve` and `internal/leadholder/wire` | Use a fake pane or `net.Pipe`. Drop the reader, scroll, then attach a fresh viewer. Assert that history and snapshot share a position, subsequent output continues there once, bounds hold and a stale ticket gets neither. |
| Console terminal history | `internal/leadattach`, `internal/terminalconsole` | Feed snapshot, rows and live frames through an in-memory peer. Restore missed rows once, page to the visible oldest marker, refuse a truncated transfer and clear the pane on detach. Count zero writes of input. |
| Scratch conversation read | Tagged `internal/leadchat`, `internal/leadcontrol` and `internal/leaddaemon` adapter, desk read values in `internal/leadwire` | With fake reply and command sources, interleave a reply, an instruction, its result and a withheld interval. Assert provenance, unique stable IDs, count/age gaps, closed reads on off or suspension, and zero inject or control calls. |
| Seat conversation IDs | `internal/approval/ownerwire`, seat gate and `internal/seatapp` | Give the seat and desk the same fixture page and compare whole message IDs. Change segment with a read in flight, then revoke. Assert stale rows never render, back navigation needs a fresh read and history triggers no decision. |
| Console conversation page | `internal/consoleclient` and `internal/terminalconsole` | Drive the model with a fake read port. Page back while new replies arrive, check provenance and all four page states, then revoke with a read pending. No stale content, mutation or approval action may survive. |
