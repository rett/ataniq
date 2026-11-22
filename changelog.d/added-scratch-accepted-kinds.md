- The scratch settings (`owner/scratch.json`) take an optional `"kinds"`: the
  kinds of credential the project accepts for a phone's login and decision
  (`platform-passkey`, `vault-passkey`, `passkey`), every kind when absent or
  empty. A name that is not a kind, or a kind named twice, refuses the
  settings. Kinds are claims of the phone, so the policy is advisory.
