- Add the message and the result to the control wire of the lead holder: an
  `inject` request may carry a `message_id` and a `text`, both or neither and on
  no other operation, and an ok response may carry an `inject` result with a
  closed outcome, reason and written amount. The holder still answers `inject`
  with `disabled`; the lead injection guide describes the format.
