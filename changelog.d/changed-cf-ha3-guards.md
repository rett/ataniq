- The tagged daemon's scratch guard (`ataniqd run --owner-channel`) now also
  refuses a drop folder that is, lies inside or contains a default state or
  runtime folder or the state folder (with its `owner/` folder), a sandbox
  template inside the drop folder, the mirror or the work folder, and a work
  folder that is, lies inside or contains the drop folder. The reason given for
  keeping the template out of the state folder is corrected in the guide.
