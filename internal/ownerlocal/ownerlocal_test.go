package ownerlocal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "https://secret-origin.example"

// home makes a configuration folder and returns its getenv and the path of
// the origin file in it.
func home(t *testing.T) (getenv func(string) string, file string) {
	t.Helper()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "ataniq", "owner-local")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_CONFIG_HOME": cfg}
	return func(k string) string { return env[k] }, filepath.Join(dir, SeatOriginName)
}

func write(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// The process's umask may have cleared bits.
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
}

func TestSeatOriginReadsTheFirstLineTrimmed(t *testing.T) {
	for _, c := range []struct{ name, content, want string }{
		{"plain", secret, secret},
		{"newline", secret + "\n", secret},
		{"crlf", secret + "\r\n", secret},
		{"spaces", "  " + secret + " \t\n", secret},
		{"several lines", secret + "\nhttps://other.example\nmore\n", secret},
		{"first line empty", "\n" + secret + "\n", ""},
		{"empty file", "", ""},
		{"spaces only", "   \n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			getenv, file := home(t)
			write(t, file, c.content, 0o600)
			got, err := SeatOrigin(getenv)
			if err != nil || got != c.want {
				t.Fatalf("SeatOrigin = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestSeatOriginOfAMissingFileIsEmptyWithNoError(t *testing.T) {
	getenv, _ := home(t)
	if got, err := SeatOrigin(getenv); got != "" || err != nil {
		t.Fatalf("SeatOrigin = %q, %v", got, err)
	}
	// No folder at all.
	empty := t.TempDir()
	if got, err := SeatOrigin(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return empty
		}
		return ""
	}); got != "" || err != nil {
		t.Fatalf("SeatOrigin = %q, %v", got, err)
	}
	// Nowhere to look.
	if got, err := SeatOrigin(func(string) string { return "" }); got != "" || err != nil {
		t.Fatalf("SeatOrigin = %q, %v", got, err)
	}
}

func TestSeatOriginRefusesWhatAnOwnerDoesNotWrite(t *testing.T) {
	t.Run("a link", func(t *testing.T) {
		getenv, file := home(t)
		target := filepath.Join(filepath.Dir(file), "elsewhere")
		write(t, target, secret+"\n", 0o600)
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
		if got, err := SeatOrigin(getenv); got != "" || !errors.Is(err, ErrNotRegular) {
			t.Fatalf("SeatOrigin = %q, %v", got, err)
		}
	})
	t.Run("a dangling link", func(t *testing.T) {
		getenv, file := home(t)
		if err := os.Symlink(filepath.Join(filepath.Dir(file), "nowhere"), file); err != nil {
			t.Fatal(err)
		}
		if got, err := SeatOrigin(getenv); got != "" || !errors.Is(err, ErrNotRegular) {
			t.Fatalf("SeatOrigin = %q, %v", got, err)
		}
	})
	t.Run("a folder", func(t *testing.T) {
		getenv, file := home(t)
		if err := os.Mkdir(file, 0o700); err != nil {
			t.Fatal(err)
		}
		if got, err := SeatOrigin(getenv); got != "" || !errors.Is(err, ErrNotRegular) {
			t.Fatalf("SeatOrigin = %q, %v", got, err)
		}
	})
	t.Run("too big", func(t *testing.T) {
		getenv, file := home(t)
		write(t, file, secret+"\n"+strings.Repeat("x", 1100), 0o600)
		if got, err := SeatOrigin(getenv); got != "" || !errors.Is(err, ErrTooBig) {
			t.Fatalf("SeatOrigin = %q, %v", got, err)
		}
	})
	t.Run("exactly 1 KiB is read", func(t *testing.T) {
		getenv, file := home(t)
		write(t, file, secret+"\n"+strings.Repeat("x", 1024-len(secret)-1), 0o600)
		if got, err := SeatOrigin(getenv); got != secret || err != nil {
			t.Fatalf("SeatOrigin = %q, %v", got, err)
		}
	})
	for _, mode := range []os.FileMode{0o620, 0o602, 0o660, 0o666} {
		t.Run("writable "+mode.String(), func(t *testing.T) {
			getenv, file := home(t)
			write(t, file, secret+"\n", mode)
			if got, err := SeatOrigin(getenv); got != "" || !errors.Is(err, ErrWritable) {
				t.Fatalf("SeatOrigin = %q, %v", got, err)
			}
		})
	}
	for _, mode := range []os.FileMode{0o400, 0o600, 0o640, 0o644} {
		t.Run("readable "+mode.String(), func(t *testing.T) {
			getenv, file := home(t)
			write(t, file, secret+"\n", mode)
			if got, err := SeatOrigin(getenv); got != secret || err != nil {
				t.Fatalf("SeatOrigin = %q, %v", got, err)
			}
		})
	}
}

// TestNoErrorHoldsTheValueOrThePath holds that what a refusal says is fixed
// text: neither the origin nor the folder it came from is in it.
func TestNoErrorHoldsTheValueOrThePath(t *testing.T) {
	getenv, file := home(t)
	write(t, file, secret+"\n"+strings.Repeat("x", 1100), 0o600)
	_, err := SeatOrigin(getenv)
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "secret-origin") || strings.Contains(err.Error(), filepath.Dir(file)) {
		t.Fatalf("error = %q", err)
	}
	write(t, file, secret+"\n", 0o666)
	_, err = SeatOrigin(getenv)
	if err == nil || strings.Contains(err.Error(), "secret-origin") || strings.Contains(err.Error(), filepath.Dir(file)) {
		t.Fatalf("error = %v", err)
	}
}

func TestSeatOriginPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "HOME": "/h"}, "/x/cfg/ataniq/owner-local/seat-origin"},
		{"home", map[string]string{"HOME": "/h"}, "/h/.config/ataniq/owner-local/seat-origin"},
		{"a relative xdg is ignored", map[string]string{"XDG_CONFIG_HOME": "cfg", "HOME": "/h"}, "/h/.config/ataniq/owner-local/seat-origin"},
		{"an empty xdg is ignored", map[string]string{"XDG_CONFIG_HOME": "", "HOME": "/h"}, "/h/.config/ataniq/owner-local/seat-origin"},
		{"a relative home", map[string]string{"HOME": "h"}, ""},
		{"nothing", map[string]string{}, ""},
	} {
		if got := SeatOriginPath(env(c.env)); got != c.want {
			t.Errorf("%s: SeatOriginPath = %q, want %q", c.name, got, c.want)
		}
	}
}
