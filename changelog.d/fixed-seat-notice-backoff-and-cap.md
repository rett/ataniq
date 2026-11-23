- The seat's notices back off exponentially after a failed call to the notifier
  (a minute, then 2, 4, 8, 16 and 30 minutes, ended by a call the notifier
  took) and count only the calls the notifier answered, a count taken or a
  refusal with a code such as `rate_limited`, toward the 30 a day. A notifier
  that is down for half an hour no longer uses up the day's notices; calls that
  got no answer are held to 200 a day. Before, every call counted and a fixed
  minute's wait spent the 30 in half an hour.
