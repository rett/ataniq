// Package leadrole defines the identity of a lead role: a project lead
// ("<project>/lead"), the supervisor ("supervisor") or the coordinator of one
// feature of a project ("<project>/coord/<feature>"). It is pure: it does no
// I/O and never touches the file system, systemd or a process.
//
// A Role is only a name with a fixed mapping to a systemd unit name and a lock
// file path. A coordinator is not a lead: it has no seat and no authority, and
// its Parent is the lead of its project. The identity of one instance of a
// coordinator, "<project>-lead@e<parent epoch>:coord:<feature>:<n>", is only an
// identifier.
//
// A Role does not prove that a unit exists, that a lock is held, or that
// anyone is allowed to act as the role; those are checked by other layers. A Role is never a runtime (a provider or a model).
//
// This package is provisional and unqualified: it is part of the lead daemon
// work in progress and no stability promise is made for its names.
package leadrole
