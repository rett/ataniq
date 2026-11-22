package noaicheck

import (
	"errors"
	"testing"
)

func TestParseProcAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:0016":                         "127.0.0.1:22",
		"00000000:01BB":                         "0.0.0.0:443",
		"00000000000000000000000000000000:0050": "[::]:80",
		"00000000000000000000000001000000:1F90": "[::1]:8080",
		"0000000000000000FFFF00000100007F:0035": "[::ffff:127.0.0.1]:53",
	} {
		got, err := parseProcAddr(in)
		if err != nil || got.String() != want {
			t.Errorf("parseProcAddr(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0100007F", "0100007F:", "0100007F:10000", "01007F:0016", "zz00007F:0016"} {
		if _, err := parseProcAddr(in); err == nil {
			t.Errorf("parseProcAddr(%q) accepted", in)
		}
	}
}

func TestCheckPorts(t *testing.T) {
	port := func(p string) Hit { return Hit{Kind: KindPort, Path: p} }
	listen := func(addr string) string {
		return "   9: " + addr + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 9 1 0 100 0 0 10 0\n"
	}
	listen6 := func(addr string) string {
		return "   9: " + addr + " 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 9 1 0 100 0 0 10 0\n"
	}
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"a listening port not allowed", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader+listen("00000000:0016")+listen("0100007F:2328"))
		}, []Hit{port("tcp:127.0.0.1:9000")}},
		{"a listening tcp6 port", func(f *fixture) {
			f.file("proc/net/tcp6", tcp6Header+listen6("00000000000000000000000001000000:0BB8"))
		}, []Hit{port("tcp6:[::1]:3000")}},
		{"a tcp6 table with the tcp header", func(f *fixture) {
			f.file("proc/net/tcp6", tcpHeader+listen6("00000000000000000000000001000000:0BB8"))
		}, []Hit{{Kind: KindIncomplete, Path: "/proc/net/tcp6", Detail: DetailHeader}}},
		{"a tcp table with the tcp6 header", func(f *fixture) {
			f.file("proc/net/tcp", tcp6Header+listen("00000000:0050"))
		}, []Hit{{Kind: KindIncomplete, Path: "/proc/net/tcp", Detail: DetailHeader}}},
		{"an address of the other family", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader+listen6("00000000000000000000000001000000:0BB8"))
			f.file("proc/net/tcp6", tcp6Header+listen("00000000:0050"))
		}, []Hit{{Kind: KindUnreadable, Path: "/proc/net/tcp", Detail: "malformed line 2"},
			{Kind: KindUnreadable, Path: "/proc/net/tcp6", Detail: "malformed line 2"}}},
		{"a connection is not a listening socket", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader+
				"   0: 0100007F:2328 0100007F:9C40 01 00000000:00000000 00:00000000 00000000  1000        0 2 1 0 20 4 30 10 -1\n")
		}, nil},
		{"no tcp6 file", func(f *fixture) { mustRemoveAll(f, "proc/net/tcp6") }, nil},
		{"no tcp file", func(f *fixture) { mustRemoveAll(f, "proc/net/tcp") },
			[]Hit{{Kind: KindUnreadable, Path: "/proc/net/tcp", Detail: "missing"}}},
		{"a malformed line", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader+"   0: junk\n"+listen("00000000:0050"))
		}, []Hit{{Kind: KindUnreadable, Path: "/proc/net/tcp", Detail: "malformed line 2"}, port("tcp:0.0.0.0:80")}},
		{"an empty tcp table", func(f *fixture) { f.file("proc/net/tcp", "") },
			[]Hit{{Kind: KindIncomplete, Path: "/proc/net/tcp", Detail: DetailHeader}}},
		{"a headerless tcp table", func(f *fixture) {
			f.file("proc/net/tcp", listen("00000000:0050"))
		}, []Hit{{Kind: KindIncomplete, Path: "/proc/net/tcp", Detail: DetailHeader}}},
		{"a headerless tcp6 table", func(f *fixture) { f.file("proc/net/tcp6", "\n") },
			[]Hit{{Kind: KindIncomplete, Path: "/proc/net/tcp6", Detail: DetailHeader}}},
		{"a header and no listeners", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader)
			f.file("proc/net/tcp6", tcp6Header)
		}, nil},
		{"proc/net as a link to self/net", func(f *fixture) {
			mustRemoveAll(f, "proc/net")
			f.file("proc/42/net/tcp", tcpHeader+listen("00000000:0019"))
			f.link("proc/net", "self/net")
		}, []Hit{port("tcp:0.0.0.0:25")}},
	}, (*Checker).CheckPorts)
}

// Each table is held to its own protocol's header, as the kernel writes
// it: proc/net/tcp spells its third column rem_address, proc/net/tcp6
// remote_address, and neither table takes the other's.
func TestListeningNeedsItsProtocolsHeader(t *testing.T) {
	for _, in := range []string{"", "\n", "x y z w\n", "  sl  local_address rem_address\n", "  sl  local_address remote_address\n"} {
		for _, proto := range []string{"tcp", "tcp6"} {
			if _, err := listening(in, proto); !errors.Is(err, errNoHeader) {
				t.Errorf("listening(%q, %s): %v", in, proto, err)
			}
		}
	}
	for _, tc := range []struct{ header, proto, row string }{
		{tcpHeader, "tcp", "   0: 0100007F:0277 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12509 1 0000000000000000 100 0 0 10 0                     \n"},
		{tcp6Header, "tcp6", "   0: 00000000000000000000000001000000:0277 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12508 1 0000000000000000 100 0 0 10 0\n"},
	} {
		if socks, err := listening(tc.header, tc.proto); err != nil || len(socks) != 0 {
			t.Errorf("%s: a header only: %v %v", tc.proto, socks, err)
		}
		socks, err := listening(tc.header+tc.row, tc.proto)
		if err != nil || len(socks) != 1 || socks[0].Port() != 631 {
			t.Errorf("%s: a listening row: %v %v", tc.proto, socks, err)
		}
	}
	if _, err := listening(tcp6Header, "tcp"); !errors.Is(err, errNoHeader) {
		t.Errorf("the tcp6 header taken for tcp: %v", err)
	}
	if _, err := listening(tcpHeader, "tcp6"); !errors.Is(err, errNoHeader) {
		t.Errorf("the tcp header taken for tcp6: %v", err)
	}
	if _, err := listening(tcpHeader, "udp"); !errors.Is(err, errNoHeader) {
		t.Errorf("an unknown protocol: %v", err)
	}
}

func TestListeningReportsTheFirstBadLine(t *testing.T) {
	_, err := listening(tcpHeader+"x\ny\n", "tcp")
	if !errors.Is(err, errProcNet) || err.Error() != "malformed line 2" {
		t.Errorf("err = %v", err)
	}
}
