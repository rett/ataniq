package consoleclient

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
)

// These tests serve a fake home with the client protocol's own managed
// server over net.Pipe: no socket. The fake builds the directory's targets as
// the daemon's directory handler does.

var homeA, homeB = strings.Repeat("a", 64), strings.Repeat("b", 64)

// fakeHome is a ManagedHandler with a fixed directory.
type fakeHome struct {
	mu        sync.Mutex
	authority string
	rows      []clientapi.DirectoryRow
	// leadOps are the operations a lead target lists, by target; a target
	// without an entry lists none.
	leadOps map[string][]string
	handled []string
	// listed counts the calls of Capabilities; onCaps runs at the start of
	// call n.
	capCalls int
	onCaps   map[int]func(*fakeHome)
}

func newRow(authority, project, name string, epoch uint64, launched bool) clientapi.DirectoryRow {
	id := clientapi.DirectoryTarget{Authority: authority, ProjectID: project, LeadRole: project + "/lead",
		HomeGeneration: 1, Revision: 2, LaunchEpoch: epoch}
	return clientapi.DirectoryRow{DirectoryTarget: id, Target: id.ID(), Launched: launched, DisplayName: name}
}

func (f *fakeHome) dir() string {
	h := sha256.New()
	json.NewEncoder(h).Encode(f.rows)
	return "directory/" + f.authority + "/" + hex.EncodeToString(h.Sum(nil))
}

func (f *fakeHome) Capabilities(context.Context) (clientapi.TargetCapabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capCalls++
	if hook := f.onCaps[f.capCalls]; hook != nil {
		hook(f)
	}
	caps := clientapi.TargetCapabilities{f.dir(): {clientapi.OpDirectoryList}}
	for _, r := range f.rows {
		caps[r.Target] = slices.Clone(f.leadOps[r.Target])
	}
	return caps, nil
}

func (f *fakeHome) HandleManaged(_ context.Context, target, op string, _ json.RawMessage) (any, *clientapi.ErrorBody) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handled = append(f.handled, op)
	if op != clientapi.OpDirectoryList || target != f.dir() {
		return nil, &clientapi.ErrorBody{Code: clientapi.CodeForbidden, Message: "no"}
	}
	return clientapi.DirectoryListResult{Rows: slices.Clone(f.rows)}, nil
}

// wireConn records the lines the client writes.
type wireConn struct {
	net.Conn
	mu    sync.Mutex
	lines []clientapi.Request
}

func (c *wireConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r clientapi.Request
		if json.Unmarshal([]byte(line), &r) == nil {
			c.lines = append(c.lines, r)
		}
	}
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *wireConn) requests() []clientapi.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines)
}

// serveHome serves h and returns the client's end.
func serveHome(t *testing.T, h clientapi.ManagedHandler) *wireConn {
	t.Helper()
	server := clientapi.NewManagedServer(h, clientapi.ManagedOptions{
		ResolvePrincipal: func(context.Context, net.Conn) (clientapi.Principal, error) {
			return clientapi.Principal{Issuer: "fixture", ClientID: "reader"}, nil
		},
	})
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.ServeConn(context.Background(), peer) }()
	t.Cleanup(func() { client.Close(); <-done })
	return &wireConn{Conn: client}
}

func readHome(t *testing.T, h clientapi.ManagedHandler) (Result, *wireConn) {
	t.Helper()
	conn := serveHome(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return ReadManaged(ctx, conn), conn
}

func mustView(t *testing.T, r Result) View {
	t.Helper()
	if r.State != StateOK {
		t.Fatalf("state = %q, want ok", r.State)
	}
	return r.View
}

// v1Peer answers the first line with reply and records every line after it.
func v1Peer(t *testing.T, reply func(id string) string) (*wireConn, func() []string) {
	t.Helper()
	client, peer := net.Pipe()
	var mu sync.Mutex
	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer peer.Close()
		br := bufio.NewReader(peer)
		for n := 0; ; n++ {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if n == 0 {
				var r clientapi.Request
				json.Unmarshal([]byte(line), &r)
				peer.Write([]byte(reply(r.RequestID) + "\n"))
			}
		}
	}()
	t.Cleanup(func() { client.Close(); <-done })
	return &wireConn{Conn: client}, func() []string {
		client.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

func TestCF02Client(t *testing.T) {
	t.Run("ListOnlyCapability", func(t *testing.T) {
		f := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{
			newRow(homeA, "alpha", "Lead", 7, true), newRow(homeA, "beta", "Lead", 3, true)}}
		v := mustView(t, func() Result { r, _ := readHome(t, f); return r }())
		if len(v.Homes) != 1 || v.Homes[0].ID != HomeID(homeA) || len(v.Projects) != 2 || len(v.OfflineHomes) != 0 {
			t.Fatalf("view = %+v", v)
		}
		h := v.Homes[0]
		if !slices.Equal(h.Capabilities[h.Directory], []string{clientapi.OpDirectoryList}) || len(h.Capabilities) != 3 {
			t.Fatalf("capabilities = %v", h.Capabilities)
		}
		for i, p := range v.Projects {
			ops, ok := h.Capabilities[p.Lead.Target]
			if !ok || len(ops) != 0 || p.Lead.Target != f.rows[i].Target || p.HomeID != h.ID || !p.HasLead ||
				p.Lead.Kind != KindLead || p.Lead.Name != f.rows[i].LeadRole || p.Lead.Epoch != f.rows[i].LaunchEpoch {
				t.Fatalf("project %d = %+v, capabilities %v", i, p, h.Capabilities)
			}
		}
	})

	t.Run("EqualNamesTwoHomes", func(t *testing.T) {
		a := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{newRow(homeA, "alpha", "Lead", 7, true)}}
		b := &fakeHome{authority: homeB, rows: []clientapi.DirectoryRow{newRow(homeB, "alpha", "Lead", 7, true)}}
		ra, _ := readHome(t, a)
		rb, _ := readHome(t, b)
		v := View{}.WithHome(HomeID(homeA), ra).WithHome(HomeID(homeB), rb)
		if len(v.Projects) != 2 {
			t.Fatalf("projects = %+v", v.Projects)
		}
		p, q := v.Projects[0], v.Projects[1]
		if p.Name != q.Name || p.Lead.Name != q.Lead.Name {
			t.Fatal("expected equal display names")
		}
		if p.HomeID == q.HomeID || p.ID == q.ID || p.Lead.ID == q.Lead.ID || p.Lead.Target == q.Lead.Target {
			t.Fatalf("equal names merged identities: %+v %+v", p, q)
		}
		// Names that cleaning makes equal are not one project, at one home.
		c := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{
			newRow(homeA, "same", "Name", 1, true), newRow(homeA, "same​", "Name​", 1, true)}}
		rc, _ := readHome(t, c)
		w := mustView(t, rc)
		if len(w.Projects) != 2 || w.Projects[0].Name != w.Projects[1].Name || w.Projects[0].ID == w.Projects[1].ID ||
			w.Projects[0].Lead.ID == w.Projects[1].Lead.ID {
			t.Fatalf("cleaned names = %+v", w.Projects)
		}
	})

	t.Run("ReorderKeepsIDs", func(t *testing.T) {
		rows := []clientapi.DirectoryRow{newRow(homeA, "alpha", "Lead", 7, true), newRow(homeA, "beta", "Lead", 3, true)}
		first := mustView(t, func() Result { r, _ := readHome(t, &fakeHome{authority: homeA, rows: rows}); return r }())
		reversed := []clientapi.DirectoryRow{rows[1], rows[0]}
		second := mustView(t, func() Result { r, _ := readHome(t, &fakeHome{authority: homeA, rows: reversed}); return r }())
		if len(first.Projects) != 2 || len(second.Projects) != 2 {
			t.Fatal("lost a project")
		}
		for i, p := range first.Projects {
			q := second.Projects[1-i]
			if p.ID != q.ID || p.Lead.ID != q.Lead.ID || p.HomeID != q.HomeID || p.Lead.Target != q.Lead.Target || p.Name != q.Name {
				t.Fatalf("identity moved with the order: %+v vs %+v", p, q)
			}
		}
	})

	t.Run("HomeOfflineKeepsStale", func(t *testing.T) {
		a := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{newRow(homeA, "alpha", "Lead", 7, true)}}
		b := &fakeHome{authority: homeB, rows: []clientapi.DirectoryRow{newRow(homeB, "beta", "Lead", 2, true)}}
		ra, _ := readHome(t, a)
		rb, _ := readHome(t, b)
		v := View{}.WithHome(HomeID(homeA), ra).WithHome(HomeID(homeB), rb)

		// Home A answers nothing: its connection is closed.
		gone, peer := net.Pipe()
		peer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		failed := ReadManaged(ctx, gone)
		if failed.State == StateOK {
			t.Fatal("a closed connection gave a view")
		}
		w := v.WithHome(HomeID(homeA), failed)
		if len(w.Projects) != 2 || !w.Projects[0].Stale || w.Projects[0].HomeID != HomeID(homeA) || w.Projects[1].Stale ||
			w.Projects[0].Lead.Target != v.Projects[0].Lead.Target || w.Projects[0].ID != v.Projects[0].ID ||
			!slices.Equal(w.OfflineHomes, []string{HomeID(homeA)}) || !w.Homes[0].Offline || w.Homes[1].Offline ||
			len(w.Homes[0].Capabilities) != 2 {
			t.Fatalf("after the outage = %+v", w)
		}
		// The view it came from is unchanged.
		if v.Projects[0].Stale || len(v.OfflineHomes) != 0 || v.Homes[0].Offline {
			t.Fatalf("the source view changed: %+v", v)
		}
		// A second outage does not duplicate anything; a read of the
		// wrong home does not replace the rows either.
		again := w.WithHome(HomeID(homeA), failed).WithHome(HomeID(homeA), rb)
		if len(again.Projects) != 2 || len(again.OfflineHomes) != 1 || !again.Projects[0].Stale {
			t.Fatalf("after more outages = %+v", again)
		}
		// A home never read is offline with no rows.
		never := v.WithHome(HomeID("c"), failed)
		if len(never.Projects) != 2 || !slices.Equal(never.OfflineHomes, []string{HomeID("c")}) {
			t.Fatalf("never-read home = %+v", never)
		}
		// The home comes back with a changed list: the rows are fresh.
		a.rows = []clientapi.DirectoryRow{newRow(homeA, "gamma", "Lead", 1, true)}
		back, _ := readHome(t, a)
		x := w.WithHome(HomeID(homeA), back)
		if len(x.OfflineHomes) != 0 || len(x.Projects) != 2 || x.Projects[0].Stale || x.Projects[0].Lead.Name != "gamma/lead" {
			t.Fatalf("after recovery = %+v", x)
		}
		// A home that answers with no leads has none: not stale rows.
		a.rows = nil
		empty, _ := readHome(t, a)
		y := x.WithHome(HomeID(homeA), empty)
		if len(y.Projects) != 1 || y.Projects[0].HomeID != HomeID(homeB) || len(y.OfflineHomes) != 0 {
			t.Fatalf("after an empty list = %+v", y)
		}
	})

	t.Run("NoV1Fallback", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// A peer without version 2 answers with its own protocol.
		conn, lines := v1Peer(t, func(id string) string {
			return `{"protocol":"ataniq.client/1","request_id":"` + id + `","ok":false,"error":{"code":"unsupported-protocol","message":"x","supported_protocols":["ataniq.client/1"]}}`
		})
		r := ReadManaged(ctx, conn)
		sent := lines()
		if r.State != StateOtherProtocol || r.View.Homes != nil || len(r.View.Projects) != 0 {
			t.Fatalf("result = %+v", r)
		}
		if len(sent) != 1 || !strings.Contains(sent[0], `"protocol":"ataniq.client/2"`) || strings.Contains(sent[0], `ataniq.client/1"`) {
			t.Fatalf("lines sent = %q", sent)
		}
		// A peer that answers hello as version 1 is a bad answer, and asked
		// nothing more.
		conn, lines = v1Peer(t, func(id string) string {
			return `{"protocol":"ataniq.client/1","request_id":"` + id + `","ok":true,"result":{"protocols":["ataniq.client/1"]}}`
		})
		r = ReadManaged(ctx, conn)
		if sent = lines(); r.State != StateBadAnswer || len(sent) != 1 {
			t.Fatalf("v1 hello: %q, %q", r.State, sent)
		}
		// A version 2 hello that does not list version 2.
		conn, lines = v1Peer(t, func(id string) string {
			return `{"protocol":"ataniq.client/2","request_id":"` + id + `","ok":true,"result":{"protocols":["ataniq.client/1"],"limits":{},"capabilities":{}}}`
		})
		r = ReadManaged(ctx, conn)
		if sent = lines(); r.State != StateOtherProtocol || len(sent) != 1 {
			t.Fatalf("hello without v2: %q, %q", r.State, sent)
		}
	})

	t.Run("ZeroWriterCalls", func(t *testing.T) {
		a := newRow(homeA, "alpha", "Lead", 7, true)
		f := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{a},
			// The home lists a writer operation for the lead.
			leadOps: map[string][]string{a.Target: {"writer.release", "conversation.read"}}}
		r, conn := readHome(t, f)
		v := mustView(t, r)
		if ops := v.Homes[0].Capabilities[a.Target]; !slices.Equal(ops, []string{"writer.release", "conversation.read"}) {
			t.Fatalf("capabilities not recorded: %v", ops)
		}
		reqs := conn.requests()
		if len(reqs) != 2 || reqs[0].Op != clientapi.OpHello || reqs[1].Op != clientapi.OpDirectoryList {
			t.Fatalf("requests = %+v", reqs)
		}
		for _, req := range reqs {
			if req.Protocol != clientapi.ProtocolV2 || (req.Op == clientapi.OpDirectoryList) != (req.Target != "") {
				t.Fatalf("request = %+v", req)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !slices.Equal(f.handled, []string{clientapi.OpDirectoryList}) {
			t.Fatalf("operations the home handled = %v", f.handled)
		}
	})

	t.Run("NeverLaunchedRowEpochUnset", func(t *testing.T) {
		f := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{
			newRow(homeA, "fresh", "Lead", 0, false), newRow(homeA, "going", "Lead", 1, true)}}
		v := mustView(t, func() Result { r, _ := readHome(t, f); return r }())
		if len(v.Projects) != 2 {
			t.Fatalf("projects = %+v", v.Projects)
		}
		fresh, going := v.Projects[0].Lead, v.Projects[1].Lead
		if !fresh.NeverLaunched || fresh.Epoch != 0 || going.NeverLaunched || going.Epoch != 1 {
			t.Fatalf("fresh = %+v, going = %+v", fresh, going)
		}
		// A row whose two facts disagree is not shown, and is counted.
		g := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{
			newRow(homeA, "a", "Lead", 5, false), newRow(homeA, "b", "Lead", 0, true), newRow(homeA, "c", "Lead", 2, true)}}
		w := mustView(t, func() Result { r, _ := readHome(t, g); return r }())
		if len(w.Projects) != 1 || w.Projects[0].Lead.Epoch != 2 || w.LeftOut != 2 || w.Homes[0].LeftOut != 2 {
			t.Fatalf("contradictory rows = %+v", w)
		}
	})

	t.Run("StaleTargetAsksAgain", func(t *testing.T) {
		f := &fakeHome{authority: homeA, rows: []clientapi.DirectoryRow{newRow(homeA, "alpha", "Lead", 7, true)}}
		// Capabilities runs twice for the hello; the list's own check is the
		// third call, where the directory changes.
		f.onCaps = map[int]func(*fakeHome){3: func(f *fakeHome) {
			f.rows = append(f.rows, newRow(homeA, "beta", "Lead", 1, true))
		}}
		r, conn := readHome(t, f)
		if v := mustView(t, r); len(v.Projects) != 2 || len(conn.requests()) != 4 {
			t.Fatalf("view = %+v, requests = %+v", v, conn.requests())
		}
	})

	t.Run("ContradictionsAreBadAnswers", func(t *testing.T) {
		good := newRow(homeA, "alpha", "Lead", 7, true)
		foreign := newRow(homeB, "alpha", "Lead", 7, true)
		wrongTarget := good
		wrongTarget.Target = "lead/other"
		for name, rows := range map[string][]clientapi.DirectoryRow{
			"RowOfAnotherHome": {foreign},
			"TargetNotFields":  {wrongTarget},
			"SameTargetTwice":  {good, good},
		} {
			f := &fakeHome{authority: homeA, rows: rows}
			if r, _ := readHome(t, f); r.State != StateBadAnswer {
				t.Errorf("%s: state = %q", name, r.State)
			}
		}
	})

	t.Run("NoDirectoryNoView", func(t *testing.T) {
		r, conn := readHome(t, &fakeHome{authority: homeA})
		// With no rows the fake still lists the directory: an empty home.
		if v := mustView(t, r); len(v.Homes) != 1 || len(v.Projects) != 0 || len(conn.requests()) != 2 {
			t.Fatalf("empty home = %+v", v)
		}
		// A principal that sees nothing gets a view with no home.
		r, conn = readHome(t, noCapabilities{})
		if v := mustView(t, r); len(v.Homes) != 0 || len(conn.requests()) != 1 {
			t.Fatalf("no capabilities = %+v", v)
		}
	})

	t.Run("RefusedAndBadTransport", func(t *testing.T) {
		server := clientapi.NewManagedServer(&fakeHome{authority: homeA}, clientapi.ManagedOptions{
			ResolvePrincipal: func(context.Context, net.Conn) (clientapi.Principal, error) {
				return clientapi.Principal{}, context.Canceled
			},
		})
		client, peer := net.Pipe()
		go server.ServeConn(context.Background(), peer)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if r := ReadManaged(ctx, client); r.State != StateRefused || r.Text == "" || r.NextStep == "" {
			t.Fatalf("unresolved principal = %+v", r)
		}
		// A peer that never answers is slow.
		quiet, _ := net.Pipe()
		short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer stop()
		if r := ReadManaged(short, quiet); r.State != StateSlow {
			t.Fatalf("silent peer = %+v", r)
		}
	})
}

// noCapabilities is a home whose principal sees nothing.
type noCapabilities struct{}

func (noCapabilities) Capabilities(context.Context) (clientapi.TargetCapabilities, error) {
	return clientapi.TargetCapabilities{}, nil
}

func (noCapabilities) HandleManaged(context.Context, string, string, json.RawMessage) (any, *clientapi.ErrorBody) {
	return nil, &clientapi.ErrorBody{Code: clientapi.CodeForbidden, Message: "no"}
}
