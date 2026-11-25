//go:build linux && ataniq_canary

package sandboxstart

import "ataniq/internal/leadsandbox/profile"

// BindTable is the bind table a start of the inputs builds, row by row with
// each row's concrete host source: the profile's inputs made from the file's
// values with the code of the start's validate step (checkedProfileInputs),
// then profile.Inputs.Binds, as the start does. It reads no file and starts
// nothing. It is exported for the tests of other packages that must hold a
// host path to every source a lead's sandbox binds (the reach of the scratch
// leads in package leaddaemon), so that they read the holder's own table and
// not a copy of its inputs. The caller sets AgentEnv as the holder does. uid
// and gid are the calling user's.
func BindTable(in Inputs, uid, gid uint32) ([]profile.Bind, error) {
	pin, err := in.checkedProfileInputs(uid, gid)
	if err != nil {
		return nil, err
	}
	return pin.Binds()
}
