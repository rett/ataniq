- The tagged daemon's `owner/scratch.json` now also refuses a repeated key that
  differs only in case (`drop` and `Drop`), which the decoder takes for the
  same key.
