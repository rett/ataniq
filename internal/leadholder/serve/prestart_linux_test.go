//go:build linux

package serve

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
	"ataniq/packs/holder"
)

func TestRegisteredIdentities(t *testing.T) {
	build := buildid.Identity{Version: "v1.2.3", Revision: "abc123"}
	got, err := registeredIdentities(build)
	if err != nil {
		t.Fatal(err)
	}
	r := pack.New()
	if err := holder.Register(r); err != nil {
		t.Fatal(err)
	}
	var want []wire.PackIdentity
	for _, name := range r.Names() {
		files := map[string][]byte{}
		for _, path := range []string{pack.ManifestPath, pack.HooksPath, pack.SandboxPath} {
			data, err := os.ReadFile(filepath.Join("../../../packs", string(name), path))
			if path != pack.ManifestPath && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			files[path] = data
		}
		digest, err := pack.DeclarationDigest(files)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, wire.PackIdentity{Name: string(name) + "@v1.2.3+abc123", DeclarationDigest: digest})
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("registered identities: got %+v, want %+v", got, want)
	}
}

func TestLocalHolderHello(t *testing.T) {
	id, err := buildid.OfProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	want := wire.HolderBuild{Module: id.Module, Version: id.Version, Revision: id.Revision, Modified: id.Modified}
	cfg := Config{}
	cfg.withDefaults()
	h, err := cfg.holderHello()
	if err != nil || h.Build != want || h.Range != (wire.VersionRange{Min: wire.ControlVersion, Max: wire.PreStartVersion}) {
		t.Fatalf("local hello %+v, %v", h, err)
	}
	if h.Packs == nil || h.HookFrames == nil || len(h.HookFrames) != 0 || h.Session || h.SpecDigest != "" {
		t.Fatalf("unexpected local capabilities: %+v", h)
	}
}

func preStartFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../wire/testdata/prestart", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func preStartConfig(t *testing.T) (Config, []byte) {
	t.Helper()
	h, err := wire.DecodeControlHello(preStartFixture(t, "hello-v2"))
	if err != nil {
		t.Fatal(err)
	}
	want := wire.LaunchRequirements{
		Build: h.Build, Pack: h.Packs[0], SpecDigest: h.SpecDigest, HookFrame: 1, Session: true,
	}
	h.SpecDigest, h.Selected = "", 0 // The service fills these from the intent and offer.
	return Config{
		Epoch: testEpoch, Launch: want, HandshakeTimeout: patience,
		holderHello: func() (wire.ControlHello, error) { return h, nil },
	}, preStartFixture(t, "launch-spec-v1")
}

// The same exchange runs over an authenticated socket in TestPreStartUnix.
func pipeControl(t *testing.T, s *Server) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		s.controlExchange(server)
	}()
	t.Cleanup(func() {
		client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(patience)); err != nil {
		t.Fatal(err)
	}
	return client
}

func controlRoundTrip(t *testing.T, c net.Conn, payload []byte) []byte {
	t.Helper()
	if err := wire.Write(c, wire.ControlRequests, wire.TypeRequest, payload); err != nil {
		t.Fatal(err)
	}
	f, err := wire.NewReader(c, wire.ControlResponses).Next()
	if err != nil {
		t.Fatal(err)
	}
	return f.Payload
}

func expectDecision(t *testing.T, c net.Conn, payload []byte, code string) {
	t.Helper()
	b := controlRoundTrip(t, c, payload)
	resp, err := wire.DecodeResponse(b)
	if err != nil || resp.OK != (code == "") || resp.Code != code {
		t.Fatalf("decision %s, %v; want code %q", b, err, code)
	}
}

func TestPreStartControl(t *testing.T) {
	cfg, spec := preStartConfig(t)
	s := &Server{cfg: cfg, sess: &session.Session{}, evq: make(chan evItem, eventQueue)}
	offer := preStartFixture(t, "offer-v2")
	c := pipeControl(t, s)
	if b := controlRoundTrip(t, c, offer); !bytes.Equal(b, preStartFixture(t, "hello-v2")) {
		t.Fatalf("N/N hello: %s", b)
	}
	// Another connection, even while this hello is live, must start over.
	expectDecision(t, pipeControl(t, s), spec, string(wire.RefuseHelloRequired))
	expectDecision(t, c, spec, "")

	t.Run("exact bytes", func(t *testing.T) {
		c := pipeControl(t, s)
		controlRoundTrip(t, c, offer)
		expectDecision(t, c, append(bytes.Clone(spec), ' '), string(wire.RefuseSpecDigest))
	})
	t.Run("unregistered declaration", func(t *testing.T) {
		cfg := cfg
		cfg.Launch.Pack.DeclarationDigest = fmt.Sprintf("%064x", 1)
		other := bytes.Replace(spec, []byte(s.cfg.Launch.Pack.DeclarationDigest), []byte(cfg.Launch.Pack.DeclarationDigest), 1)
		cfg.Launch.SpecDigest = fmt.Sprintf("%x", sha256.Sum256(other))
		c := pipeControl(t, &Server{cfg: cfg, sess: s.sess, evq: s.evq})
		controlRoundTrip(t, c, offer)
		expectDecision(t, c, other, string(wire.RefusePack))
	})
	t.Run("unknown pack in spec", func(t *testing.T) {
		cfg := cfg
		other := bytes.Replace(spec, []byte(cfg.Launch.Pack.Name), []byte("other@1.0.0+r1"), 1)
		cfg.Launch.SpecDigest = fmt.Sprintf("%x", sha256.Sum256(other))
		c := pipeControl(t, &Server{cfg: cfg, sess: s.sess, evq: s.evq})
		controlRoundTrip(t, c, offer)
		expectDecision(t, c, other, string(wire.RefusePack))
	})
	t.Run("other epoch", func(t *testing.T) {
		cfg := cfg
		cfg.Epoch++
		c := pipeControl(t, &Server{cfg: cfg, sess: s.sess, evq: s.evq})
		controlRoundTrip(t, c, offer)
		expectDecision(t, c, spec, wire.CodeWrongEpoch)
	})
	for _, tc := range []struct {
		name                 string
		daemonMax, holderMax uint16
	}{
		{"previous daemon range", 1, 2},
		{"previous holder range", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfg
			h, err := cfg.holderHello()
			if err != nil {
				t.Fatal(err)
			}
			h.Range.Max = tc.holderMax
			cfg.holderHello = func() (wire.ControlHello, error) { return h, nil }
			c := pipeControl(t, &Server{cfg: cfg, sess: s.sess, evq: s.evq})
			req := wire.ControlOffer{Version: wire.PreStartVersion, Range: wire.VersionRange{Min: 1, Max: tc.daemonMax}}
			p, err := wire.EncodeControlOffer(req)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := wire.DecodeControlHello(controlRoundTrip(t, c, p))
			if err != nil || reply.Selected != wire.ControlVersion || reply.Range != h.Range {
				t.Fatalf("display hello %+v, %v", reply, err)
			}
			expectDecision(t, c, spec, string(wire.RefuseLaunchVersion))
		})
	}
	t.Run("disjoint", func(t *testing.T) {
		expectDecision(t, pipeControl(t, s), []byte(`{"version":2,"range":{"min":3,"max":3}}`), string(wire.RefuseNoVersion))
	})
	t.Run("legacy daemon", func(t *testing.T) {
		// A legacy request gets exactly one v1 reply and EOF.
		c := pipeControl(t, s)
		b := controlRoundTrip(t, c, []byte(`{"op":"ticket","epoch":1}`))
		if string(b) != `{"ok":false,"code":"bad-request","text":"the request is not valid"}` {
			t.Fatalf("legacy refusal: %s", b)
		}
		if _, err := wire.NewReader(c, wire.ControlResponses).Next(); err != io.EOF {
			t.Fatalf("after v1 answer: %v", err)
		}
		c = pipeControl(t, s)
		b = controlRoundTrip(t, c, []byte(`{"op":"release-writer","epoch":7}`))
		if string(b) != `{"ok":true}` {
			t.Fatalf("legacy command: %s", b)
		}
	})
}

func TestPreStartUnix(t *testing.T) {
	cfg, spec := preStartConfig(t)
	e := newEnv(t, "raw; say READY; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.Launch, c.holderHello = cfg.Launch, cfg.holderHello }
	})
	c, err := sockDial(e.dir, ControlSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(patience)); err != nil {
		t.Fatal(err)
	}
	if b := controlRoundTrip(t, c, preStartFixture(t, "offer-v2")); !bytes.Equal(b, preStartFixture(t, "hello-v2")) {
		t.Fatalf("hello: %s", b)
	}
	expectDecision(t, c, spec, "")
}
