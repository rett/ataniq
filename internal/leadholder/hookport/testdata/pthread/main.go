//go:build ignore

// This HP-8 child creates a thread through the linked C library after
// installing the production holder filter. Build it by filename with cgo.
package main

/*
#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <sys/socket.h>
#include <unistd.h>
static void *check_socket(void *unused) {
 int fd = socket(AF_UNIX, SOCK_STREAM, 0);
 if (fd >= 0) { close(fd); return 0; }
 return (void *)(intptr_t)errno;
}
static int check_thread(void) {
 pthread_t id;
 int err = pthread_create(&id, 0, check_socket, 0);
 if (err) return -err;
 void *result;
 err = pthread_join(id, &result);
 return err ? -err : (int)(intptr_t)result;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"ataniq/internal/leadholder/hookport"
)

func main() {
	filter, err := hookport.InstallFilter()
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			fmt.Printf("INSTALL %d\n", errno)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("READY %x\n", filter.Digest())
	if n := C.check_thread(); n != C.EPERM {
		fmt.Fprintf(os.Stderr, "new C thread socket result %d\n", n)
		os.Exit(1)
	}
	fmt.Println("RESULT 0")
}
