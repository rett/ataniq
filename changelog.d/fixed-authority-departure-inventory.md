- Authority host switches and rollbacks keep decisions inhibited when a legacy
  deployment lacks its service inventory. After stopping the services, the owner
  can record the inventory from the original import with
  `ah-switch recover --services-stopped` and retry the departure.
