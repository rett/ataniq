//go:build linux && ataniq_canary

// Package injectbridge joins the holder's session to the injection library.
// It is compiled in canary mode only.
//
// The library (package inject) delivers one message through three interfaces
// it asks its holder to implement: Session, Locked and View. The holder's
// session (package session) offers the same three things in types of its own,
// because it imports neither the library nor its parser. This package is the
// adapter between them, and the only place the two meet:
//
//   - [Bridge] is the library's Session over one session.Session. Its
//     WithInputLock is the session's WithFence; inside it the library is given
//     a Locked over session.Fenced, and inside Frozen a View over
//     session.FenceView. Every promise the library's interfaces make is kept
//     by the session's fence (see session/fence_linux.go); the bridge adds no
//     lock, no goroutine, no wait and no write of its own, and it passes every
//     error on unchanged, so that an ended context is still found with
//     errors.Is and a session error is still the session's.
//   - [Hook] is the session's handler for the OSC 7501 status protocol: it
//     answers the support query with the library's reply and gives every
//     payload to the tracker whose status the View reports. It is installed
//     before the agent starts (session.Config.OSC), with the tracker and the
//     clock that then go into [Config].
//   - [WireResult] is the one mapping from the library's result to the words
//     of the control wire.
//   - [Deliverer] delivers one message for the control server: it checks the
//     header, runs the library's fence once and maps the result. Its method
//     set is the control server's deliverer; this package does not import the
//     server.
//   - [Counts] reads the tracker's totals for the holder's counters.
//
// The package starts nothing and keeps no state beyond the tracker it is
// given.
package injectbridge
