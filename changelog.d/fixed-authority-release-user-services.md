- Authority-host release validation, the standalone no-AI check and switch
  import now refuse user services and their drop-ins under `etc/systemd/user/`
  with `user-service`. The switch checks departing services through the system
  manager.
