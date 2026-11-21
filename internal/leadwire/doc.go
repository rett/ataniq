// Package leadwire holds the wire values of the lead control plane: the
// names of its six operations, the params and results of each, the [Receipt]
// that a mutating operation answers with, and the closed sets of words they
// use. The values are plain structs and constants. The package has no
// behaviour beyond naming, no build tag, and imports only the standard
// library, so the daemon and the console's client share one definition of
// the JSON without sharing code.
//
// The operations are carried by the client protocol's extension mechanism
// (the operation names have one dot, as that mechanism requires):
//
//	lead.list    no params      -> [ListResult]
//	lead.define  [DefineParams] -> [Receipt] with a [DefineResult]
//	lead.start   [StartParams]  -> [Receipt] with a [StartResult]
//	lead.stop    [StopParams]   -> [Receipt] with a [StopResult]
//	lead.ticket  [TicketParams] -> [Receipt] with a [TicketResult]
//	lead.inject  [InjectParams] -> [Receipt] with an [InjectResult]
//
// Every key of a params struct is required, so no field is omitted when it is
// empty or zero. A mutating operation's params begin with the client and the
// command id, then the role, then the rest in the order of the struct; the
// order of the fields is part of the contract, because the daemon takes the
// digest of a command from the params as they are marshalled, with the
// client and the command id zeroed. A result's [Receipt.Result] holds the
// JSON of the result struct of the operation.
//
// Nothing here checks a value. The daemon decodes params strictly and judges
// them; the values below are the shapes and the caps it judges them by. The
// client string is a namespace the console generates for one run, not an
// identity.
package leadwire
