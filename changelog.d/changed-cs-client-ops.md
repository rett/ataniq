- Let the client protocol package carry extension operations that the program
  serving it registers. A request may carry `params`, one JSON object; `hello`
  lists the extension operations after `hello` and `status` and says
  `mutations: true` only when a registered extension changes something, and the
  package gains a `Call` method for them. No release build registers one, so
  `hello` still lists two operations and says `mutations: false`.
