package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

type directoryFixture struct {
	entries []leadregistry.Entry
	homes   map[string]leadregistry.ProjectAssignment
	scope   DirectoryScope
	onList  func()
	// revision stands for the journal sequence; a test that changes the
	// fixture between two reads also moves it, as a real change does.
	revision uint64
	// afterList runs once List has copied its entries, where a registry change
	// could fall between List and ProjectHome.
	afterList func()
}

func (f *directoryFixture) Revision() (uint64, error) { return f.revision, nil }

func (f *directoryFixture) List() ([]leadregistry.Entry, error) {
	if f.onList != nil {
		f.onList()
	}
	out := slices.Clone(f.entries)
	if f.afterList != nil {
		f.afterList()
	}
	return out, nil
}

func (f *directoryFixture) ProjectHome(project string) (leadregistry.ProjectAssignment, error) {
	if a, ok := f.homes[project]; ok {
		return a, nil
	}
	return leadregistry.ProjectAssignment{}, leadregistry.ErrHomeMissing
}

var directoryReader = clientapi.Principal{Issuer: "fixture", ClientID: "reader"}

func newDirectoryFixture(t *testing.T) (*DirectoryHandler, *directoryFixture) {
	t.Helper()
	return newDirectoryFixtureAt(t, strings.Repeat("a", 64))
}

func newDirectoryFixtureAt(t *testing.T, authority string) (*DirectoryHandler, *directoryFixture) {
	t.Helper()
	id := instanceid.Identity{Binding: instanceid.Binding{Authority: authority}}
	f := &directoryFixture{homes: map[string]leadregistry.ProjectAssignment{}, scope: DirectoryScope{
		"alpha": "Lead", "beta": "Lead",
	}}
	for _, project := range []string{"alpha", "beta", "private"} {
		role, err := leadrole.Lead(project)
		if err != nil {
			t.Fatal(err)
		}
		f.entries = append(f.entries, leadregistry.Entry{Role: role, Epoch: 7})
		f.homes[project] = leadregistry.ProjectAssignment{
			ProjectVersion: leadregistry.ProjectVersion{HomeGeneration: 2, Revision: 3},
			HomeAuthority:  id.Authority, ExecutingInstance: id.Authority,
			Holder: &leadregistry.LeadRef{Authority: id.Authority, Role: role},
		}
	}
	return NewDirectoryHandler(f, id, func(p clientapi.Principal) DirectoryScope {
		if p == directoryReader {
			return f.scope
		}
		return nil
	}), f
}

// Preserve a context supplied by the real server for direct handler tests.
// Only the connection verifier supplies the principal.
type directoryContextProbe struct {
	*DirectoryHandler
	contexts chan context.Context
}

func (p directoryContextProbe) Capabilities(ctx context.Context) (clientapi.TargetCapabilities, error) {
	select {
	case p.contexts <- ctx:
	default:
	}
	return p.DirectoryHandler.Capabilities(ctx)
}

type directoryStream struct {
	ctx    context.Context
	caps   clientapi.TargetCapabilities
	target string
	ask    func(string, string) clientapi.Response
}

func openDirectoryStream(t *testing.T, h *DirectoryHandler) directoryStream {
	t.Helper()
	probe := directoryContextProbe{h, make(chan context.Context, 1)}
	server := clientapi.NewManagedServer(probe, clientapi.ManagedOptions{
		ResolvePrincipal: func(context.Context, net.Conn) (clientapi.Principal, error) { return directoryReader, nil },
	})
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeConn(context.Background(), peer)
	}()
	t.Cleanup(func() {
		client.Close()
		<-done
	})
	encode, decode := json.NewEncoder(client), json.NewDecoder(client)
	ask := func(target, op string) clientapi.Response {
		t.Helper()
		if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		req := clientapi.Request{Protocol: clientapi.ProtocolV2, RequestID: "r", Target: target, Op: op}
		if err := encode.Encode(req); err != nil {
			t.Fatal(err)
		}
		var res clientapi.Response
		if err := decode.Decode(&res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := ask("", clientapi.OpHello)
	var hello clientapi.ManagedHelloResult
	if !res.OK || json.Unmarshal(res.Result, &hello) != nil {
		t.Fatalf("hello = %+v", res)
	}
	for target, ops := range hello.Capabilities {
		if slices.Contains(ops, clientapi.OpDirectoryList) {
			return directoryStream{<-probe.contexts, hello.Capabilities, target, ask}
		}
	}
	t.Fatalf("directory missing from hello: %v", hello.Capabilities)
	return directoryStream{}
}

func (s directoryStream) list(t *testing.T) []clientapi.DirectoryRow {
	t.Helper()
	res := s.ask(s.target, clientapi.OpDirectoryList)
	var result clientapi.DirectoryListResult
	if !res.OK || json.Unmarshal(res.Result, &result) != nil || result.Rows == nil {
		t.Fatalf("directory.list = %+v", res)
	}
	for _, row := range result.Rows {
		if ops, ok := s.caps[row.Target]; !ok || len(ops) != 0 || row.Target != row.DirectoryTarget.ID() {
			t.Fatalf("row target %q, capabilities %v", row.Target, s.caps)
		}
	}
	if len(s.caps) != len(result.Rows)+1 {
		t.Fatalf("hello/list disagreement: %v, %+v", s.caps, result)
	}
	return result.Rows
}

func TestCF02(t *testing.T) {
	t.Run("ReadOnlyCapsHaveNoWriterOps", func(t *testing.T) {
		h, _ := newDirectoryFixture(t)
		s := openDirectoryStream(t, h)
		if !slices.Equal(s.caps[s.target], []string{clientapi.OpDirectoryList}) {
			t.Fatalf("directory capabilities = %v", s.caps)
		}
		rows := s.list(t)
		if len(rows) != 2 || rows[0].ProjectID != "alpha" || rows[1].ProjectID != "beta" {
			t.Fatalf("reader sees %+v", rows)
		}
		for _, row := range rows {
			if row.ConversationAvailable || row.ConversationID != "" || row.LaunchEpoch != 7 ||
				row.Revision != 3 || row.HomeGeneration != 2 || row.Authority != h.authority ||
				row.LeadRole != row.ProjectID+"/lead" {
				t.Fatalf("directory row = %+v", row)
			}
		}
		res := s.ask(rows[0].Target, "writer.release")
		if res.OK || res.Error == nil || res.Error.Code != clientapi.CodeForbidden {
			t.Fatalf("writer request = %+v", res)
		}
		caps, err := h.Capabilities(context.Background())
		if err != nil || len(caps) != 0 {
			t.Fatalf("capabilities without principal = %v, %v", caps, err)
		}
	})

	t.Run("EqualNamesDistinctTargets", func(t *testing.T) {
		// One project id with one display name at two homes: the names and
		// ids are equal and only the home tells the rows apart.
		var rows []clientapi.DirectoryRow
		for _, authority := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
			h, _ := newDirectoryFixtureAt(t, authority)
			got := openDirectoryStream(t, h).list(t)
			if len(got) != 2 || got[0].ProjectID != "alpha" {
				t.Fatalf("home %s lists %+v", authority, got)
			}
			rows = append(rows, got[0])
		}
		if rows[0].DisplayName != rows[1].DisplayName || rows[0].ProjectID != rows[1].ProjectID ||
			rows[0].Authority == rows[1].Authority || rows[0].Target == rows[1].Target {
			t.Fatalf("equal names at two homes = %+v", rows)
		}
	})

	t.Run("ReorderStableTargets", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		s := openDirectoryStream(t, h)
		before := s.list(t)
		slices.Reverse(f.entries)
		after := s.list(t)
		if len(before) != 2 || !slices.Equal(before, after) {
			t.Fatalf("reorder: %+v => %+v", before, after)
		}
	})

	t.Run("MissingProjectDropped", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		delete(f.homes, "alpha")
		a := f.homes["beta"]
		a.Holder, a.ExecutingInstance = nil, ""
		f.homes["beta"] = a
		if rows := openDirectoryStream(t, h).list(t); len(rows) != 0 {
			t.Fatalf("missing and vacant assignments = %+v", rows)
		}
	})

	// Each of the four checks of a visible row fails alone: the others hold.
	t.Run("ForeignHomeHidden", func(t *testing.T) {
		foreign := strings.Repeat("b", 64)
		for name, change := range map[string]func(*leadregistry.ProjectAssignment){
			"HomeAuthority":     func(a *leadregistry.ProjectAssignment) { a.HomeAuthority = foreign },
			"HolderAuthority":   func(a *leadregistry.ProjectAssignment) { a.Holder.Authority = foreign },
			"ExecutingInstance": func(a *leadregistry.ProjectAssignment) { a.ExecutingInstance = foreign },
			"NoHolder":          func(a *leadregistry.ProjectAssignment) { a.Holder = nil },
		} {
			t.Run(name, func(t *testing.T) {
				h, f := newDirectoryFixture(t)
				a := f.homes["beta"]
				if a.Holder != nil {
					held := *a.Holder
					a.Holder = &held
				}
				change(&a)
				f.homes["beta"] = a
				if rows := openDirectoryStream(t, h).list(t); len(rows) != 1 || rows[0].ProjectID != "alpha" {
					t.Fatalf("%s: assignment = %+v", name, rows)
				}
			})
		}
	})

	t.Run("HolderRoleMismatchHidden", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		other, err := leadrole.Lead("alpha")
		if err != nil {
			t.Fatal(err)
		}
		a := f.homes["beta"]
		a.Holder = &leadregistry.LeadRef{Authority: h.authority, Role: other}
		f.homes["beta"] = a
		if rows := openDirectoryStream(t, h).list(t); len(rows) != 1 || rows[0].ProjectID != "alpha" {
			t.Fatalf("holder of another role = %+v", rows)
		}
	})

	t.Run("OnlyLeadKindListed", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		coordinator, err := leadrole.Coordinator("alpha", "feature")
		if err != nil {
			t.Fatal(err)
		}
		f.entries = append(f.entries, leadregistry.Entry{Role: coordinator, Epoch: 4}, leadregistry.Entry{Role: leadrole.Supervisor(), Epoch: 1})
		// The coordinator shares its project's home, which is visible.
		rows := openDirectoryStream(t, h).list(t)
		if len(rows) != 2 || rows[0].LeadRole != "alpha/lead" || rows[1].LeadRole != "beta/lead" {
			t.Fatalf("kinds listed = %+v", rows)
		}
	})

	t.Run("NeverLaunchedLeadIsNotEpochZero", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		f.entries[1].Epoch = 0
		rows := openDirectoryStream(t, h).list(t)
		if len(rows) != 2 || !rows[0].Launched || rows[0].LaunchEpoch != 7 || rows[1].Launched || rows[1].LaunchEpoch != 0 {
			t.Fatalf("launched and never launched = %+v", rows)
		}
	})

	t.Run("RowsFromOneRegistryState", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		// A launch of alpha and a new revision of its assignment land between
		// List and ProjectHome, once. The first read has the old epoch with
		// the new revision; the handler must not return that row.
		moved := false
		f.afterList = func() {
			if moved {
				return
			}
			moved = true
			f.revision++
			f.entries[0].Epoch = 8
			a := f.homes["alpha"]
			a.Revision = 4
			f.homes["alpha"] = a
		}
		rows := openDirectoryStream(t, h).list(t)
		if !moved || len(rows) != 2 || rows[0].LaunchEpoch != 8 || rows[0].Revision != 4 {
			t.Fatalf("rows after a change between the reads = %+v", rows)
		}
		// A registry that changes during every attempt gives no rows.
		f.afterList = func() { f.revision++ }
		if _, err := h.rows(f.scope); !errors.Is(err, errDirectoryMoving) {
			t.Fatalf("rows of a moving registry: %v", err)
		}
	})

	t.Run("PolicyChangeMidCallForbidden", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		s := openDirectoryStream(t, h)
		if result, fail := h.HandleManaged(s.ctx, s.target, clientapi.OpDirectoryList, nil); fail != nil || result == nil {
			t.Fatalf("authorized direct read = %+v, %+v", result, fail)
		}
		f.onList = func() { f.scope = DirectoryScope{"alpha": "Lead"} }
		result, fail := h.HandleManaged(s.ctx, s.target, clientapi.OpDirectoryList, nil)
		if result != nil || fail == nil || fail.Code != clientapi.CodeForbidden {
			t.Fatalf("direct read after narrowing = %+v, %+v", result, fail)
		}
	})

	t.Run("StaleListTargetForbidden", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		s := openDirectoryStream(t, h)
		f.scope = DirectoryScope{"alpha": ""}
		res := s.ask(s.target, clientapi.OpDirectoryList)
		if res.OK || res.Result != nil || res.Error == nil || res.Error.Code != clientapi.CodeForbidden {
			t.Fatalf("stale directory.list = %+v", res)
		}
		rows := openDirectoryStream(t, h).list(t)
		if len(rows) != 1 || rows[0].ProjectID != "alpha" || rows[0].DisplayName != "alpha" {
			t.Fatalf("fresh directory.list = %+v", rows)
		}
	})
}
