package leadattach

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/wire"
)

func ticketDir(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, "ticket")
}

func writeTicketFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func newTicket(t *testing.T) string {
	t.Helper()
	s, err := wire.NewTicket()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFileTicketsReadsOnceAndEmptiesTheFile(t *testing.T) {
	_, path := ticketDir(t)
	src := FileTickets(path)
	if _, err := src(); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("absent file: %v", err)
	}
	tk := newTicket(t)
	writeTicketFile(t, path, tk+"\n", 0o600)
	got, err := src()
	if err != nil || got != tk {
		t.Fatalf("ticket %q, %v", got, err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("the file holds %q (%v) after it was read", b, err)
	}
	if _, err := src(); !errors.Is(err, ErrNoTicket) {
		t.Errorf("a second read: %v, want no ticket", err)
	}
	// An empty file is no ticket.
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file was replaced: %v %v", fi, err)
	}
	// A new ticket in the same file is read again.
	tk2 := newTicket(t)
	writeTicketFile(t, path, tk2, 0o600)
	if got, err := src(); err != nil || got != tk2 {
		t.Errorf("second ticket %q, %v", got, err)
	}
}

func TestFileTicketsRefusals(t *testing.T) {
	tk := newTicket(t)
	for _, c := range []struct {
		name  string
		setup func(dir, path string)
		want  string
		kept  bool // the file's content is left as it was
	}{
		{"mode 0644", func(_, p string) { writeTicketFile(t, p, tk, 0o644) }, "mode is not 0600", true},
		{"mode 0660", func(_, p string) { writeTicketFile(t, p, tk, 0o660) }, "mode is not 0600", true},
		{"mode 0400", func(_, p string) { writeTicketFile(t, p, tk, 0o400) }, "mode is not 0600", true},
		{"mode 0700", func(_, p string) { writeTicketFile(t, p, tk, 0o700) }, "mode is not 0600", true},
		{"a symbolic link", func(d, p string) {
			real := filepath.Join(d, "real")
			writeTicketFile(t, real, tk, 0o600)
			if err := os.Symlink(real, p); err != nil {
				t.Fatal(err)
			}
		}, "symbolic link", true},
		{"a dangling symbolic link", func(d, p string) {
			if err := os.Symlink(filepath.Join(d, "nowhere"), p); err != nil {
				t.Fatal(err)
			}
		}, "symbolic link", true},
		{"a directory", func(_, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file", true},
		{"a named pipe", func(_, p string) {
			if err := mkfifo(p); err != nil {
				t.Skip("no mkfifo")
			}
		}, "not a regular file", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, path := ticketDir(t)
			c.setup(dir, path)
			got, err := FileTickets(path)()
			if err == nil || errors.Is(err, ErrNoTicket) || got != "" {
				t.Fatalf("got %q, %v; want a refusal", got, err)
			}
			if !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q", err)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("the error names the path: %q", err)
			}
			if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
				if b, _ := os.ReadFile(path); string(b) != tk {
					t.Errorf("a refused file was changed: %q", b)
				}
			}
		})
	}
}

func TestFileTicketsOpensWithoutSideEffects(t *testing.T) {
	for _, f := range []struct {
		name string
		flag int
	}{
		{"O_NOCTTY", unix.O_NOCTTY}, {"O_NONBLOCK", unix.O_NONBLOCK},
		{"O_NOFOLLOW", unix.O_NOFOLLOW}, {"O_CLOEXEC", unix.O_CLOEXEC},
	} {
		if ticketOpenFlags&f.flag == 0 {
			t.Errorf("the ticket file is opened without %s", f.name)
		}
	}
	// A device is refused from its type, before anything is read from it:
	// /dev/null reads as an empty file, which would be "no ticket yet".
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}
	_, err := readTicketFile("/dev/null", os.Geteuid())
	if err == nil || errors.Is(err, ErrNoTicket) || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a character device: %v", err)
	}
}

func TestFileTicketsRefusesAnotherOwner(t *testing.T) {
	_, path := ticketDir(t)
	tk := newTicket(t)
	writeTicketFile(t, path, tk, 0o600)
	if _, err := readTicketFile(path, os.Geteuid()+1); err == nil || !strings.Contains(err.Error(), "not owned by the user") {
		t.Errorf("another owner: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != tk {
		t.Errorf("a refused file was emptied: %q", b)
	}
	if got, err := readTicketFile(path, os.Geteuid()); err != nil || got != tk {
		t.Errorf("the owner: %q, %v", got, err)
	}
}

func TestFileTicketsSpendsAFileThatHoldsJunk(t *testing.T) {
	for name, content := range map[string]string{
		"not a ticket":    "hello",
		"upper-case":      strings.ToUpper(newTicket(t)),
		"two tickets":     newTicket(t) + "\n" + newTicket(t),
		"too long":        strings.Repeat("a", 5000),
		"a binary string": "\x00\x01\x02",
	} {
		t.Run(name, func(t *testing.T) {
			_, path := ticketDir(t)
			writeTicketFile(t, path, content, 0o600)
			src := FileTickets(path)
			got, err := src()
			if err == nil || errors.Is(err, ErrNoTicket) || got != "" || !strings.Contains(err.Error(), "emptied") {
				t.Fatalf("got %q, %v", got, err)
			}
			// It is spent, so it does not wedge the client.
			if _, err := src(); !errors.Is(err, ErrNoTicket) {
				t.Errorf("after the junk: %v", err)
			}
		})
	}
}

func TestFileTicketsOneReaderAtATime(t *testing.T) {
	_, path := ticketDir(t)
	tk := newTicket(t)
	writeTicketFile(t, path, tk, 0o600)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flockNB(f); err != nil {
		t.Fatal(err)
	}
	if _, err := FileTickets(path)(); !errors.Is(err, ErrNoTicket) {
		t.Errorf("a locked file: %v", err)
	}
	f.Close()
	if got, err := FileTickets(path)(); err != nil || got != tk {
		t.Errorf("after the lock: %q, %v", got, err)
	}
}

const modeRefused = "ticket file refused: its mode is not 0600"

// The check names a refusal in the same words as a read of the file, which is
// what the client shows, and a file that holds no ticket is ErrNoTicket.
func TestFileTicketSourceReadyIsAPresenceCheck(t *testing.T) {
	tk := newTicket(t)
	for _, c := range []struct {
		name  string
		setup func(dir, path string)
		want  string // "" is nil (dial), "none" is ErrNoTicket, else the reason
	}{
		{"absent", func(_, _ string) {}, "none"},
		{"empty", func(_, p string) { writeTicketFile(t, p, "", 0o600) }, "none"},
		{"a ticket", func(_, p string) { writeTicketFile(t, p, tk, 0o600) }, ""},
		{"a ticket and a newline", func(_, p string) { writeTicketFile(t, p, tk+"\n", 0o600) }, ""},
		{"junk (the source spends it)", func(_, p string) { writeTicketFile(t, p, "junk", 0o600) }, ""},
		{"mode 0644", func(_, p string) { writeTicketFile(t, p, tk, 0o644) }, modeRefused},
		{"mode 0400", func(_, p string) { writeTicketFile(t, p, tk, 0o400) }, modeRefused},
		{"mode 0700", func(_, p string) { writeTicketFile(t, p, tk, 0o700) }, modeRefused},
		{"set-group-id", func(_, p string) { writeTicketFile(t, p, tk, 0o600|os.ModeSetgid) }, modeRefused},
		{"a symbolic link to a good file", func(d, p string) {
			real := filepath.Join(d, "real")
			writeTicketFile(t, real, tk, 0o600)
			if err := os.Symlink(real, p); err != nil {
				t.Fatal(err)
			}
		}, "ticket file refused: it is a symbolic link"},
		{"a dangling symbolic link", func(d, p string) {
			if err := os.Symlink(filepath.Join(d, "nowhere"), p); err != nil {
				t.Fatal(err)
			}
		}, "ticket file refused: it is a symbolic link"},
		{"a directory", func(_, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "ticket file refused: it is not a regular file"},
		{"a named pipe", func(_, p string) {
			if err := mkfifo(p); err != nil {
				t.Skip("no mkfifo")
			}
		}, "ticket file refused: it is not a regular file"},
		{"a wrong mode and no content", func(_, p string) { writeTicketFile(t, p, "", 0o644) }, modeRefused},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, path := ticketDir(t)
			c.setup(dir, path)
			_, ready := FileTicketSource(path)
			// Asked again and again, it says the same and changes nothing.
			for i := 0; i < 3; i++ {
				got := ready()
				switch {
				case c.want == "" && got != nil, c.want == "none" && !errors.Is(got, ErrNoTicket):
					t.Fatalf("ready is %v, want %q", got, c.want)
				case c.want != "" && c.want != "none" && (got == nil || got.Error() != c.want):
					t.Fatalf("ready is %v, want the reason %q", got, c.want)
				}
			}
			if c.want != "" && c.want != "none" {
				// The same reason as the read of the file gives.
				if _, rerr := FileTickets(path)(); rerr == nil || rerr.Error() != c.want {
					t.Errorf("the read gives %v, want %q", rerr, c.want)
				}
			}
			if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
				if b, _ := os.ReadFile(path); len(b) == 0 {
					t.Errorf("the check emptied the file")
				}
			}
		})
	}
}

func TestFileTicketSourceReadyDoesNotTakeTheTicket(t *testing.T) {
	_, path := ticketDir(t)
	tickets, ready := FileTicketSource(path)
	if err := ready(); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("ready for an absent file: %v", err)
	}
	tk := newTicket(t)
	writeTicketFile(t, path, tk+"\n", 0o600)
	for i := 0; i < 5; i++ {
		if err := ready(); err != nil {
			t.Fatalf("not ready for a file that holds a ticket: %v", err)
		}
	}
	// The ticket is there for the one read, and then it is gone.
	if got, err := tickets(); err != nil || got != tk {
		t.Fatalf("ticket %q, %v", got, err)
	}
	if err := ready(); !errors.Is(err, ErrNoTicket) {
		t.Errorf("after the ticket was taken: %v", err)
	}
	// A reader that holds the lock does not make the check say no: the
	// source then finds no ticket, and the client closes the connection.
	writeTicketFile(t, path, newTicket(t), 0o600)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flockNB(f); err != nil {
		t.Fatal(err)
	}
	if err := ready(); err != nil {
		t.Errorf("the check is affected by the lock of another reader: %v", err)
	}
}

func TestFileTicketSourceReadyRefusesAnotherOwner(t *testing.T) {
	_, path := ticketDir(t)
	writeTicketFile(t, path, newTicket(t), 0o600)
	if err := ticketFilePresent(path, os.Geteuid()+1); err == nil ||
		err.Error() != "ticket file refused: it is not owned by the user" {
		t.Errorf("a file of another owner: %v", err)
	}
	if err := ticketFilePresent(path, os.Geteuid()); err != nil {
		t.Errorf("a file of the user: %v", err)
	}
}
