package leaddaemon

import (
	"context"
	"encoding/json"
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
}

func (f *directoryFixture) List() ([]leadregistry.Entry, error) {
	if f.onList != nil {
		f.onList()
	}
	return f.entries, nil
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
	id := instanceid.Identity{Binding: instanceid.Binding{Authority: strings.Repeat("a", 64)}}
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
		h, _ := newDirectoryFixture(t)
		rows := openDirectoryStream(t, h).list(t)
		if len(rows) != 2 || rows[0].DisplayName != rows[1].DisplayName || rows[0].Target == rows[1].Target {
			t.Fatalf("equal display names = %+v", rows)
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

	t.Run("ForeignHomeHidden", func(t *testing.T) {
		h, f := newDirectoryFixture(t)
		a := f.homes["beta"]
		foreign := strings.Repeat("b", 64)
		a.HomeAuthority, a.Holder.Authority, a.ExecutingInstance = foreign, foreign, foreign
		f.homes["beta"] = a
		if rows := openDirectoryStream(t, h).list(t); len(rows) != 1 || rows[0].ProjectID != "alpha" {
			t.Fatalf("foreign assignment = %+v", rows)
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
