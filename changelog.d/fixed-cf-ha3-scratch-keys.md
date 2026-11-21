- The tagged daemon's `owner/scratch.json` now refuses a key that appears twice
  in one object, as its documentation said; the decoder used to keep the last
  value without a word.
