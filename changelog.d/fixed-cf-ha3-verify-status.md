- The tagged daemon's host adapter now tells a lead that a verification could
  not run, not that its gates failed, when the verification script ended with
  one of its own statuses 90 to 93 (the pin moved, the fetch or the checkout
  failed, or the commit is not a merge on the base). The run is still kept as
  a record and is no pass.
