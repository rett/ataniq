- The console's live attach pin now treats epoch 0 and an empty lead as
  pinned values, completes the pin only after a Welcome passes the role
  check, and stops when a ticket source reports a stale selection on the
  scratch attach path too, instead of polling for tickets.
