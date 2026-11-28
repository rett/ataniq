- The console's live attach client now drops input still waiting to be sent
  when it loses the writer role or its connection is replaced, instead of
  letting it reach a screen the user no longer sees. It counts the drop as
  `client-input-fenced` (also `InputFenced` in the live status) and says so in
  one notice. `Config.ReconnectAsViewer` makes the client reconnect as a viewer
  after such a loss, with `Config.ViewerTickets` for the viewer tickets; the
  default keeps the scratch `--attach` behaviour. Queued input given up with a
  connection is now counted as fenced, no longer as `client-input-dropped`,
  which counts only a full queue.
