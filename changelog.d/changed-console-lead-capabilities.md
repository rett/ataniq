- The daemon console opens the lead directory whenever the daemon offers
  `lead.list`. The client checks each command against the current connection's
  offer and keeps the original command ID if a reconnect prevents a retry.
