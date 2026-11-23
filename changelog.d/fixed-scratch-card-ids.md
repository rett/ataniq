- The scratch pass of the tagged build (`make ownerchannel-build`) numbers the
  cards of its stand-in door from the time of day and past the highest number
  its journal holds for the day, not from 1 on every start, so that a restart
  on the same state folder no longer makes a card with the id of one that was
  decided, which could not be decided itself.
