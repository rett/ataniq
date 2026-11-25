package agentsession

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func requireError(t *testing.T, err error, member string, reason InputReason) {
	t.Helper()
	var e *InputError
	if !errors.Is(err, ErrInput) || !errors.As(err, &e) || e.Member != member || e.Reason != reason || err.Error() != "agentsession: invalid input: "+member+": "+string(reason) {
		t.Fatalf("got %v, want %s %s", err, member, reason)
	}
}
func TestTR32TR37PolicySources(t *testing.T) {
	var sets []struct {
		Entries       []struct{ Kind, Name, Raw, Content string }
		Frame, Digest string
	}
	b, err := os.ReadFile("sessiontest/testdata/policy-sources.golden")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &sets); err != nil {
		t.Fatal(err)
	}
	for _, set := range sets {
		var entries []PolicySource
		for _, e := range set.Entries {
			source, err := PolicySourceFromBytes(e.Kind, e.Name, []byte(e.Raw))
			if err != nil || hex.EncodeToString(source.Content[:]) != e.Content {
				t.Fatal(e, err)
			}
			again, _ := PolicySourceFromBytes(e.Kind, e.Name, bytes.Clone([]byte(e.Raw)))
			if source != again {
				t.Fatal(e)
			}
			entries = append(entries, source)
		}
		for range 2 {
			before := slices.Clone(entries)
			frame, err := sourceFrame(entries)
			if err != nil || hex.EncodeToString(frame) != set.Frame {
				t.Fatal(set, err)
			}
			digest, err := PolicySourcesDigest(entries)
			if err != nil || hex.EncodeToString(digest[:]) != set.Digest || !reflect.DeepEqual(entries, before) {
				t.Fatal(set, err)
			}
			slices.Reverse(entries)
		}
	}
	a, _ := PolicySourceFromBytes("ab", "c", nil)
	b2, _ := PolicySourceFromBytes("a", "bc", nil)
	da, _ := PolicySourcesDigest([]PolicySource{a})
	db, _ := PolicySourcesDigest([]PolicySource{b2})
	if da == db {
		t.Fatal("split names collide")
	}
	_, err = PolicySourcesDigest([]PolicySource{a, a})
	requireError(t, err, "PolicySources[1].Name", "duplicate")
	for _, tc := range []struct {
		s string
		r InputReason
	}{{"", "empty-value"}, {strings.Repeat("a", 129), "too-long"}, {"a\x00b", "not-printable"}, {"\u0085", "not-printable"}, {"\xff", "not-printable"}} {
		_, err := PolicySourceFromBytes("a", tc.s, nil)
		requireError(t, err, "PolicySource.Name", tc.r)
	}
}
