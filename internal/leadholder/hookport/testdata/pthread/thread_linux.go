//go:build cgo

// Package pthread checks the linked C library's real thread creation path.
package pthread

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

const Linked = true

func Check() int { return int(C.check_thread()) }
