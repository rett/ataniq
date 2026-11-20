- Fix the attended canary run's step 26 so that an end of input (Ctrl-D, or a
  terminal that hangs up) at one of its Enters still records a failure when a
  daemon still answers or a root still exists, as it did before the step began
  to ask again. A missing key no longer hides either.
