package sandboxnet

import (
	"reflect"
	"strings"
	"testing"
)

func TestPastaArgsGolden(t *testing.T) {
	want := []string{"-f", "-q", "--config-net", "-t", "none", "-u", "none", "-T", "none", "-U", "none", "--no-map-gw", "--"}
	if got := PastaArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// The contract line, as the task graph writes it.
	line := PastaProgram + " " + strings.Join(PastaArgs(), " ")
	if want := "pasta -f -q --config-net -t none -u none -T none -U none --no-map-gw --"; line != want {
		t.Errorf("command line %q want %q", line, want)
	}
}

func TestPastaArgsFreshSlice(t *testing.T) {
	a := PastaArgs()
	a[0] = "changed"
	a = append(a[:1], "extra")
	b := PastaArgs()
	if b[0] != "-f" || len(b) != 13 {
		t.Errorf("a later call saw the earlier change: %q", b)
	}
	// Appending to a result never writes into a shared array.
	c, d := PastaArgs(), PastaArgs()
	c = append(c, "cmd")
	if len(d) != 13 || c[len(c)-1] != "cmd" {
		t.Errorf("shared storage between calls")
	}
}

// The first profile forwards nothing and maps no host address.
func TestPastaArgsForwardNothing(t *testing.T) {
	args := PastaArgs()
	for _, flag := range []string{"-t", "-u", "-T", "-U"} {
		found := false
		for i, a := range args {
			if a == flag {
				found = true
				if i+1 >= len(args) || args[i+1] != "none" {
					t.Errorf("%s is not followed by none", flag)
				}
			}
		}
		if !found {
			t.Errorf("%s missing", flag)
		}
	}
	for _, a := range args {
		if a == "--map-host-loopback" || a == "--map-guest-addr" || a == "all" || a == "auto" {
			t.Errorf("forbidden argument %q", a)
		}
	}
	if args[len(args)-1] != "--" {
		t.Error("the arguments do not end with --")
	}
}
