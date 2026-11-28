- Restoring an incarnation with a journal in place now requires explicit
  acknowledgement with `--leave-old-journal`. Scratch preflight reads the
  journal header and reports a foreign binding with exit 14. Missing-key
  messages distinguish an intact root from a restore.
