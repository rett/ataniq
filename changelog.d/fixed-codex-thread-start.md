- The Codex app-server codec starts threads with a read-only sandbox and
  on-request approval. It binds to the thread returned by its start request
  and accepts one turn start per observed idle state. A failed or abandoned
  turn start releases its reservation so the holder can send another message.
