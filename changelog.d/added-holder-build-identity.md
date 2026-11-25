- The lead holder records its own build identity, read from the Go build
  information of the executable that actually runs (`/proc/<pid>/exe`, never
  the path it was started by): the main module and its version, the VCS
  revision and whether the checkout was modified, and as provenance the Go
  version and the build tags. It is the member `build` of `holder.json` and the
  field `build` of the `start` event of the holder's event log. A holder that
  cannot read it refuses to start, with exit status 2. The member is added,
  and nothing older refuses it: a daemon or `ataniq-scratch` of an earlier
  release never reads `holder.json`, and the canary run kit's
  `ataniq-canary fields` prints any member.
