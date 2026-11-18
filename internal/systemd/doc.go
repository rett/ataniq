// Package systemd holds the generic Linux and systemd building blocks a
// supervisor uses to reason about a unit it did not start itself: the identity
// of a control-group boundary on the unified hierarchy and its populated flag,
// the parser for the key=value output of "systemctl show", the check for a
// pending job, the parse of a StartTransientUnit reply into a job number, and
// a bounded command call with a fixed environment for the user manager.
//
// What it does not prove. A boundary identity is the device, inode and owner of
// a cgroup directory; it says the directory is the same one as before, not that
// a unit with a given name or invocation owns it, and a cgroup that was removed
// and recreated gets a new identity. The populated flag is one reading of
// cgroup.events: Populated reads the identity before and after it and fails
// when either differs, but a process can still enter later, so an empty answer
// is a fact about that instant only. A parsed property set says what systemd
// printed, not that the unit is the intended one. A job number in a reply says
// the manager accepted the request, not that the unit started or that it is
// the unit the caller named. A command call that returned an error may still
// have had its effect: the caller must inspect and never retry blindly.
//
// The package is provisional and unqualified: it was moved out of the
// qualification host so that other code can use it, and it has no
// qualification of its own. It builds on Linux only and has no architecture
// constraint. It talks to systemd only through the command the caller names;
// it opens no bus connection and starts nothing itself.
package systemd
