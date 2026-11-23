- The notifier's unit runs in a group of its own, `seat-notify`, not in the
  seat's group, so the notifier, the one unit with egress, cannot reach the
  seat's socket in `/run/seat` (and so cannot send the seat a client address
  of its choosing). Its socket folder and socket belong to
  `seat-notify:seat-notify` (0750 and 0660), and the seat's unit joins that
  group with `SupplementaryGroups=seat-notify`, so the group must exist on the
  host before the seat's release is switched to (the README's setup makes it,
  with the seat's user as a member and Caddy not). Tests hold that the
  notifier's unit names no group `seat` and the seat's names `seat-notify`.
