// Package leadrole defines the identity of a lead role: a project lead
// ("<project>/lead") or the supervisor ("supervisor"). It is pure: it does no
// I/O and never touches the file system, systemd or a process.
//
// A Role is only a name with a fixed mapping to a systemd unit name and a lock
// file path. It does not prove that a unit exists, that a lock is held, or
// that anyone is allowed to act as the role; those are checked by other
// layers. A Role is never a runtime (a provider or a model).
//
// This package is provisional and unqualified: it is part of the lead daemon
// work in progress and no stability promise is made for its names.
package leadrole
