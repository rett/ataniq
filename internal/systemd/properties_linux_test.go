package systemd

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseProperties(t *testing.T) {
	const required = "Id,LoadState,Job"
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want map[string]string
		err  string
	}{
		{"all keys", "Id=a.service\nLoadState=loaded\nJob=\n", 4096, map[string]string{"Id": "a.service", "LoadState": "loaded", "Job": ""}, ""},
		{"no final newline", "Id=a\nLoadState=x\nJob=0", 4096, map[string]string{"Id": "a", "LoadState": "x", "Job": "0"}, ""},
		{"value with equals", "Id=a=b\nLoadState=\nJob=1", 4096, map[string]string{"Id": "a=b", "LoadState": "", "Job": "1"}, ""},
		{"order is free", "Job=0\nId=a\nLoadState=b\n", 4096, map[string]string{"Id": "a", "LoadState": "b", "Job": "0"}, ""},
		{"too long", "Id=a\nLoadState=b\nJob=0\n", 10, nil, "show output excessive"},
		{"exactly at the cap", "Id=a\nLoadState=b\nJob=0\n", len("Id=a\nLoadState=b\nJob=0\n"), map[string]string{"Id": "a", "LoadState": "b", "Job": "0"}, ""},
		{"empty", "", 4096, nil, "malformed show output"},
		{"no equals", "Id=a\nLoadState\nJob=0\n", 4096, nil, "malformed show output"},
		{"empty key", "Id=a\n=x\nJob=0\n", 4096, nil, "malformed show output"},
		{"blank line", "Id=a\n\nJob=0\n", 4096, nil, "malformed show output"},
		{"duplicate", "Id=a\nId=b\nLoadState=x\nJob=0\n", 4096, nil, "duplicate show property"},
		{"unexpected", "Id=a\nLoadState=x\nJob=0\nExtra=1\n", 4096, nil, "unexpected show property"},
		{"prefix of a key is not a key", "Id=a\nLoad=x\nJob=0\n", 4096, nil, "unexpected show property"},
		{"missing", "Id=a\nJob=0\n", 4096, nil, "missing show property"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseProperties(tc.in, required, tc.max)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err || got != nil {
					t.Fatalf("got %v, %v; want %s", got, err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestNoJob(t *testing.T) {
	for _, tc := range []struct {
		p    map[string]string
		want bool
	}{
		{map[string]string{"Job": ""}, true},
		{map[string]string{"Job": "0"}, true},
		{map[string]string{"Job": "37"}, false},
		{map[string]string{"Job": " "}, false},
		{map[string]string{"Id": "a"}, false},
		{nil, false},
	} {
		if NoJob(tc.p) != tc.want {
			t.Errorf("%v: want %v", tc.p, tc.want)
		}
	}
}

func TestParseJobReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want uint64
		err  error
	}{
		{"job", "o \"/org/freedesktop/systemd1/job/123\"\n", 123, nil},
		{"zero", "o \"/org/freedesktop/systemd1/job/0\"", 0, nil},
		{"largest", "o \"/org/freedesktop/systemd1/job/18446744073709551615\"", 18446744073709551615, nil},
		{"empty", "", 0, ErrJobReply},
		{"wrong type", "s \"/org/freedesktop/systemd1/job/1\"", 0, ErrJobReply},
		{"no type", "\"/org/freedesktop/systemd1/job/1\"", 0, ErrJobReply},
		{"extra field", "o \"/org/freedesktop/systemd1/job/1\" x", 0, ErrJobReply},
		{"other object", "o \"/org/freedesktop/systemd1/unit/a_2eservice\"", 0, ErrJobReply},
		{"unquoted", "o /org/freedesktop/systemd1/job/1", 0, ErrJobReply},
		{"unterminated", "o \"/org/freedesktop/systemd1/job/1", 0, ErrJobReply},
		{"no number", "o \"/org/freedesktop/systemd1/job/\"", 0, ErrJobNumber},
		{"not a number", "o \"/org/freedesktop/systemd1/job/x1\"", 0, ErrJobNumber},
		{"negative", "o \"/org/freedesktop/systemd1/job/-1\"", 0, ErrJobNumber},
		{"overflow", "o \"/org/freedesktop/systemd1/job/18446744073709551616\"", 0, ErrJobNumber},
		{"nested path", "o \"/org/freedesktop/systemd1/job/1/2\"", 0, ErrJobNumber},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJobReply(tc.in)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("got %d, %v", got, err)
			}
		})
	}
}
