// Package qr encodes text as a QR code (ISO/IEC 18004 model 2) and renders
// it for the desk console and the app.
//
// It is small on purpose. It encodes in byte mode only (the bytes of the
// string are the data, whatever they are), at error correction level L or M,
// in versions 1 to 10 (21 to 57 modules a side); Encode picks the smallest
// version that holds the text and returns ErrTooLong beyond version 10. At
// level M that is 213 bytes, at level L 271. The encoder writes the function
// patterns, the Reed-Solomon codewords over GF(256), the format and version
// information, tries all eight masks and keeps the one with the lowest
// penalty by the four standard rules (the lowest mask number wins a tie).
// The result is deterministic.
//
// The package is pure: it imports only the standard library, and of that
// nothing that touches the system (no os, no net, no exec, no filepath).
// imports_test.go enforces it.
//
// # Drawing
//
// A QR code is dark modules on a light ground, and scanners expect exactly
// that, with a light quiet zone around it. HalfBlocks and PNG both add the
// quiet zone. The console must draw the rows of HalfBlocks black on white
// whatever its theme is: a theme that draws the text light on dark, or that
// inverts the screen, makes a code that most scanners read badly or not at
// all.
package qr
