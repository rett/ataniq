//go:build !linux

package mirror

import "io/fs"

// oNoFollow is not available on this system.
const oNoFollow = 0

// ownerUID reports no owner on this system, so [Create] and [Open] refuse
// every folder.
func ownerUID(fi fs.FileInfo) (uint32, bool) { return 0, false }
