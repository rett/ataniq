//go:build !cgo

package pthread

const Linked = false

func Check() int { panic("C library is not linked") }
