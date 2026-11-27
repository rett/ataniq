// The unfiltered exec control replaces the case process with this static
// image. The other modes give the kernel tests a disposable direct child.
package main

import (
	"fmt"
	"os"
	"unsafe"
)

var sample byte

func main() {
	if len(os.Args) == 1 {
		fmt.Println("EXEC")
		return
	}
	fmt.Printf("%x\n", uintptr(unsafe.Pointer(&sample)))
	_, _ = os.Stdin.Read(make([]byte, 1))
}
