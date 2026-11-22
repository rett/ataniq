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
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"a listening port not allowed", func(f *fixture) {
			f.file("proc/net/tcp", tcpHeader+listen("00000000:0016")+listen("0100007F:2328"))
		}, []Hit{port("tcp:127.0.0.1:9000")}},
		{"a listening tcp6 port", func(f *fixture) {
			f.file("proc/net/tcp6", tcpHeader+listen("00000000000000000000000001000000:0BB8"))
		}, []Hit{port("tcp6:[::1]:3000")}},
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
		{"proc/net as a link to self/net", func(f *fixture) {
			mustRemoveAll(f, "proc/net")
			f.file("proc/42/net/tcp", tcpHeader+listen("00000000:0019"))
			f.link("proc/net", "self/net")
		}, []Hit{port("tcp:0.0.0.0:25")}},
	}, (*Checker).CheckPorts)
}

func TestListeningReportsTheFirstBadLine(t *testing.T) {
	_, err := listening(tcpHeader + "x\ny\n")
	if !errors.Is(err, errProcNet) || err.Error() != "malformed line 2" {
		t.Errorf("err = %v", err)
	}
}
