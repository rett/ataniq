- The file-drop collector of the tagged daemon now answers a request that a
  stop left taken but never handled: on its first scan after the start, a
  request in the `collected` folder with no reply that neither the service nor
  the door knows gets one `request-invalid` reply asking for a new request id,
  instead of waiting for an answer that never came. A reply that exists is never
  written over, and the request is not submitted again.
