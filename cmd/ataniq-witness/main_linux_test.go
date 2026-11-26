package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/seatgate"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (config, string) {
	t.Helper()
	dir := t.TempDir()
	c := config{Identity: durable.Identity{Witness: "witness", Store: "directory", Incarnation: "first", Epoch: 7},
		Slot:  cp.Slot{Witness: "witness", Authority: "authority", Store: "ledger"},
		State: filepath.Join(dir, "state"), Seed: filepath.Join(dir, "seed"),
		Socket: filepath.Join(dir, "run", "admin.sock"), ServiceTLS: filepath.Join(dir, "tls"), OwnerTLS: filepath.Join(dir, "owner"),
		Home: "192.0.2.4:7443", HomePin: strings.Repeat("a", 64)}
	must(t, os.Mkdir(filepath.Dir(c.Socket), 0700))
	must(t, os.Mkdir(c.State, 0700))
	must(t, os.MkdirAll(filepath.Join(dir, guardDirectory), 0755))
	service, err := seatgate.WriteIdentityFiles(c.ServiceTLS, "witness")
	must(t, err)
	owner, err := seatgate.WriteIdentityFiles(c.OwnerTLS, "owner")
	must(t, err)
	c.ServicePin, c.OwnerPin = service.Fingerprint(), owner.Fingerprint()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	c.SigningKey = hex.EncodeToString(pub)
	must(t, os.WriteFile(c.Seed, key.Seed(), 0600))
	pub, _, err = ed25519.GenerateKey(rand.Reader)
	must(t, err)
	c.HomeKey = hex.EncodeToString(pub)
	path := filepath.Join(dir, "config")
	writeConfig(t, path, c)
	return c, path
}
func writeConfig(t *testing.T, path string, c config) {
	t.Helper()
	b, err := json.Marshal(c)
	must(t, err)
	must(t, os.WriteFile(path, b, 0600))
}
func call(t *testing.T, path, op string, want int) string {
	t.Helper()
	var b bytes.Buffer
	if code := run(t.Context(), filepath.Dir(path), []string{op, "--config", path}, &b, &b); code != want {
		t.Fatalf("%s: exit %d, %s", op, code, &b)
	}
	return b.String()
}

func TestWitnessInitAndReadOnlyCheck(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "check", 1)
	call(t, path, "init", 0)
	call(t, path, "init", 1)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.Open(store)
	must(t, err)
	defer w.Close()
	before, err := os.Stat(filepath.Join(c.State, "journal.v1"))
	must(t, err)
	if got := call(t, path, "check", 0); got != "witness check ok\n" {
		t.Fatal(got)
	}
	after, err := os.Stat(filepath.Join(c.State, "journal.v1"))
	must(t, err)
	if before.ModTime() != after.ModTime() {
		t.Fatal("check wrote journal")
	}
	must(t, os.Chmod(c.Seed, 0640))
	call(t, path, "check", 1)
	must(t, os.Chmod(c.Seed, 0600))
	c.SigningKey = c.HomeKey
	writeConfig(t, path, c)
	call(t, path, "check", 1)
}

func TestWitnessRefusesUnsafeInputs(t *testing.T) {
	c, path := fixture(t)
	b, err := os.ReadFile(path)
	must(t, err)
	for _, extra := range []string{`,"Socket":"private-value"}`, `,"socket":"private-value"}`, `,"extra":"private-value"}`, `} {}`} {
		must(t, os.WriteFile(path, append(bytes.Clone(b[:len(b)-1]), extra...), 0600))
		got := call(t, path, "init", 1)
		if strings.Contains(got, "private-value") || strings.Contains(got, c.HomePin) {
			t.Fatal("diagnostic exposed input")
		}
	}
	originalSocket := c.Socket
	c.Socket = "tcp://192.0.2.5:80"
	writeConfig(t, path, c)
	for _, op := range []string{"status", "acknowledge", "run"} {
		call(t, path, op, 1)
	}
	c.Socket = originalSocket
	writeConfig(t, path, c)
	must(t, os.Chmod(c.State, 0750))
	call(t, path, "init", 1)
	must(t, os.Chmod(c.State, 0700))
	must(t, os.Link(c.Seed, c.Seed+"-alias"))
	call(t, path, "init", 1)
}

func TestWitnessGuardChangesBetweenRequests(t *testing.T) {
	c, path := fixture(t)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.Create(store)
	must(t, err)
	defer w.Close()
	g := &guardedWitness{Witness: w, dir: filepath.Join(filepath.Dir(path), guardDirectory), turn: make(chan struct{}, 1)}
	must(t, w.Grant(c.Slot, store.HomePin, true))
	a := ownerAdmin{g}
	ctx := t.Context()
	guard := filepath.Join(g.dir, "decisions.inhibit")
	must(t, os.WriteFile(guard, nil, 0600))
	_, err = g.SettleHome(ctx, 7, c.Slot, cp.ID{1}, store.HomePin)
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = a.Settle(ctx, 7, c.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	if !errors.Is(a.AcknowledgeStorageError(ctx), cp.ErrUnavailable) {
		t.Fatal("acknowledged during switch")
	}
	must(t, os.Remove(guard))
	_, err = a.Settle(ctx, 7, c.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	must(t, os.Remove(g.dir))
	r, err := a.Status(ctx, cp.ID{4})
	must(t, err)
	st, err := cp.VerifyDirectoryStatus(store.Pin, 7, cp.ID{4}, r)
	must(t, err)
	if st.Inhibition != cp.ServiceInhibited || st.Fences != 1 {
		t.Fatal(st)
	}
}

func TestWitnessOwnerSocketRecovery(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	marker := filepath.Join(c.State, "poisoned")
	must(t, os.WriteFile(marker, nil, 0600))
	call(t, path, "run", 1)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.OpenForRecovery(store)
	must(t, err)
	defer w.Close()
	server, err := serviceConfig(c, store)
	must(t, err)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.Socket, Net: "unix"})
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		t.Skipf("private Unix socket denied: %v", err)
	}
	must(t, err)
	g := &guardedWitness{Witness: w, dir: filepath.Join(filepath.Dir(path), guardDirectory), recoveryOnly: true, turn: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, g, ln, c.OwnerPin, uint32(os.Geteuid())) }()
	defer func() { cancel(); must(t, <-done) }()
	if got := call(t, path, "status", 1); !strings.Contains(got, `inhibition="storage error"`) {
		t.Fatal(got)
	}
	call(t, path, "acknowledge", 0)
	if got := call(t, path, "status", 0); !strings.Contains(got, `inhibition="service inhibited"`) {
		t.Fatal(got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestWitnessHomeFirewallEndpoint(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	for _, addr := range []string{"192.0.2.20:7443", "[2001:db8::5]:7443"} {
		c.Home = addr
		writeConfig(t, path, c)
		call(t, path, "check", 0)
	}
	for _, addr := range []string{"192.0.2.5:07443", "192.000.2.5:7443", "[2001:0db8::5]:7443", "[::ffff:192.0.2.4]:7443",
		"255.255.255.255:7443", "0.1.2.3:7443", "127.0.0.1:7443", "[::1]:7443", "169.254.1.2:7443", "[fe80::1]:7443", "224.0.0.1:7443", "[ff02::1]:7443", "*:7443", ":7443", "0.0.0.0:7443", "[::]:7443", "[::ffff:0.0.0.0]:7443",
		"HOME:7443", "192.0.2.5:https", "192.0.2.5:0", "192.0.2.5:65536", "192.0.2.0/24:7443", "[2001:db8::5%if]:7443"} {
		c.Home = addr
		writeConfig(t, path, c)
		for _, op := range []string{"check", "run"} {
			if got := call(t, path, op, 1); got != "invalid witness configuration\n" {
				t.Fatalf("%q: %s", addr, got)
			}
		}
	}
}

func TestWitnessCheckEgress(t *testing.T) {
	c, configPath := fixture(t)
	call(t, configPath, "init", 0)
	dir := filepath.Join(filepath.Dir(configPath), "etc/systemd/system/ataniq-witness.service.d")
	must(t, os.MkdirAll(dir, 0755))
	rule := filepath.Join(dir, "egress.conf")
	must(t, os.WriteFile(rule, []byte("[Service]\nIPAddressAllow=192.0.2.4/32\n"), 0644))
	call(t, configPath, "check", 0)
	c.Home = "192.0.2.5:7443"
	writeConfig(t, configPath, c)
	for _, op := range []string{"check", "run"} {
		if got := call(t, configPath, op, 1); got != "invalid witness egress drop-in\n" {
			t.Fatal(got)
		}
	}
}

func TestWitnessOwnerSocketPreflight(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	for _, tc := range []struct{ name, socket string }{
		{"occupied", c.Socket}, {"missing parent", filepath.Join(filepath.Dir(c.Socket), "missing", "socket")},
		{"overlong", filepath.Join(filepath.Dir(c.Socket), strings.Repeat("x", 108))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := c
			s.Socket = tc.socket
			if tc.name == "occupied" {
				must(t, os.WriteFile(s.Socket, nil, 0600))
			}
			writeConfig(t, path, s)
			for _, op := range []string{"check", "run", "recover"} {
				if got := call(t, path, op, 1); got != "owner socket already present or unusable\n" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestWitnessStaleOwnerSocket(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	l := privateListener(t, c.Socket)
	l.SetUnlinkOnClose(false)
	must(t, l.Close())
	if got := call(t, path, "check", 1); got != "owner socket already present or unusable\n" {
		t.Fatal(got)
	}
	if _, err := os.Lstat(c.Socket); err != nil {
		t.Fatal("check removed the socket", err)
	}
}

func TestWitnessOverlappingPaths(t *testing.T) {
	c, p := fixture(t)
	for _, tc := range []struct {
		name   string
		change func(*config)
	}{
		{"socket in state", func(x *config) { x.Socket = filepath.Join(x.State, "s") }},
		{"guard in state", func(x *config) { x.State = filepath.Dir(guardDirectory) }},
		{"guard equals state", func(x *config) { x.State = guardDirectory }},
		{"seed in state", func(x *config) { x.Seed = filepath.Join(x.State, "seed") }},
		{"owner below service TLS", func(x *config) { x.OwnerTLS = filepath.Join(x.ServiceTLS, "owner") }},
		{"service below owner TLS", func(x *config) { x.ServiceTLS = filepath.Join(x.OwnerTLS, "service") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := c
			tc.change(&x)
			writeConfig(t, p, x)
			_, _, err := loadConfig(p)
			if !errors.Is(err, errConfig) {
				t.Fatal(err)
			}
		})
	}
}

func TestWitnessIdentityCharacters(t *testing.T) {
	c, p := fixture(t)
	for _, value := range []string{"space ~", "nul\x00", "tab\t", "line\n", "del\x7f", "é"} {
		for _, set := range []func(*config, string){
			func(x *config, s string) { x.Identity.Witness = s; x.Slot.Witness = s },
			func(x *config, s string) { x.Identity.Store = s },
			func(x *config, s string) { x.Identity.Incarnation = s },
			func(x *config, s string) { x.Slot.Authority = s },
			func(x *config, s string) { x.Slot.Store = s },
		} {
			x := c
			set(&x, value)
			writeConfig(t, p, x)
			_, _, err := loadConfig(p)
			if (err == nil) != (value == "space ~") {
				t.Errorf("%q: %v", value, err)
			}
		}
	}
}

func TestWitnessPinnedGuard(t *testing.T) {
	_, configPath := fixture(t)
	call(t, configPath, "init", 0)
	dir := filepath.Join(filepath.Dir(configPath), "var/lib/authority-host")
	must(t, os.Remove(dir))
	call(t, configPath, "check", 1)
	must(t, os.WriteFile(dir, nil, 0644))
	call(t, configPath, "check", 1)
	must(t, os.Remove(dir))
	must(t, os.Mkdir(dir, 0755))
	must(t, os.WriteFile(filepath.Join(dir, "decisions.inhibit"), nil, 0644))
	call(t, configPath, "check", 0)
	data, err := os.ReadFile(configPath)
	must(t, err)
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(data, &fields))
	fields["guard"] = json.RawMessage(`"/another/guard"`)
	data, err = json.Marshal(fields)
	must(t, err)
	must(t, os.WriteFile(configPath, data, 0600))
	if got := call(t, configPath, "check", 1); got != "invalid witness configuration\n" {
		t.Fatal(got)
	}
}
