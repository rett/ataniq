- The scratch settings (`owner/scratch.json`) take an optional `"kinds"`: the
  kinds of credential the project accepts for a phone's decision
  (`platform-passkey`, `vault-passkey`, `passkey`), every kind when absent or
  empty. The policy applies when a decision's challenge is asked and when the
  decision is made; a login belongs to no project, so it checks only that the
  daemon knows the key's kind. A name that is not a kind, or a kind named
  twice, refuses the settings. Kinds are claims of the phone, so the policy is
  advisory.
