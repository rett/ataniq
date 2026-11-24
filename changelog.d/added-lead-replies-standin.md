- Under the canary tag, the stand-in agent now writes one short reply to the
  lead's reply folder (`~/.ataniq-replies`) after each commit it makes
  (`Stand-in: handled <7-character commit id>.`, written to a dot-name and
  renamed to `<seq>-standin.txt`), so the outbox can be proved with no AI. It
  writes nothing when the folder is missing, is a link or cannot be written,
  or when three of its own replies are waiting, and never makes or changes the
  folder. See `docs/scratch-leads.md`, "Lead replies".
