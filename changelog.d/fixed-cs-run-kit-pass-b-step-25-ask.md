- Fix step 25 of pass B of the attended canary run: the owner's question about
  a changed login file now reads the owner's answer (it read the next line of
  the changes file), the file must match the canary's own file under the same
  name, a change is a finding without a question when the canary's own login
  wrote no file of its kind in its own folder, and the result line names the
  files the owner explained. Step 20 no longer opens a file at the filesystem
  root when the lead's folder cannot be resolved. The README names `go-on` at
  step 16 and the new judgement of step 25.
