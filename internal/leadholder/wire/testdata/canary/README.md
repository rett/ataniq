# Golden control requests of the canary slice

These files are the exact bytes of the payload of the control requests the
daemon sends to the holder in the canary slice, one file each, with no
trailing newline:

- `ticket-request.json`: the ticket request of `canary ticket` for the
  `writer` attach role, epoch 1, the first command of the epoch.
- `inject-request.json`: the inject request of `canary inject` for the message
  id `golden-msg-1` and the body `canary-token: 0123456789abcdef-golden`, epoch
  1, the second command of the epoch.

The daemon sends no status request and no stop request to the holder in the
slice (the daemon stops the lead through the unit manager), so there is no
golden file for them. The daemon test checks that these two requests are all
it sent.

## What they are for

They join the daemon layer to the holder layer of the end-to-end tests.

- The daemon layer (`internal/leaddaemon/canary_contract_test.go`) drives the
  daemon over its client socket and compares the payload of each request its
  fake control server received with the file, byte for byte.
- The holder layer and the accepting path
  (`cmd/ataniq-holder/canary_e2e_test.go`) send the same payloads, framed, to
  a real holder's control socket.

If the daemon changes what it sends, or the holder changes what it accepts,
one of the two tests fails. A change of the wire must change these files on
purpose, by hand and in the same change as the wire; no test writes them.

## The masked ticket

The ticket of a ticket request is random, so it cannot be a golden value. The
file holds 64 zeros in its place, which is a well-formed ticket. The daemon test
replaces the ticket it received (after checking that it is a well-formed ticket,
that it is not the 64 zeros and that it occurs once) with the 64 zeros before it
compares. The holder tests replace the 64 zeros with a fresh ticket before they
send. Every other byte of the request is compared as it is.

## The command ids

The command ids are the daemon's counter, `canary-<op>-<epoch>-<n>`. They are
deterministic only for a fresh daemon that sends the ticket request first and
the inject request second, as the daemon test does.
