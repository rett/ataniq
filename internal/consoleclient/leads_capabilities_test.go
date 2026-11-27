package consoleclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadwire"
)

// capabilityPeer answers calls synchronously. A nil reply simulates a lost
// answer after the request reached the peer.
type capabilityPeer func(clientapi.Request) any

func (p capabilityPeer) Call(_ context.Context, op string, params, result any) error {
	req := clientapi.Request{Op: op}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	reply := p(req)
	if reply == nil {
		return io.EOF
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

func (capabilityPeer) Close() error { return nil }

func leadsWithOffers(t *testing.T, handle func(int, clientapi.Request) any) *Leads {
	t.Helper()
	l, err := NewLeads(LeadsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var dials int
	l.dial = func(context.Context, string) (leadRPC, error) {
		dials++
		n := dials
		return capabilityPeer(func(req clientapi.Request) any { return handle(n, req) }), nil
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestReadOnlyDirectory(t *testing.T) {
	var writerCalls, viewerCalls int
	l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
		switch req.Op {
		case clientapi.OpHello:
			return map[string]any{
				"protocols":    clientapi.SupportedProtocols(),
				"operations":   []string{leadwire.OpList, leadwire.OpTicket},
				"ticket_roles": []string{leadwire.AttachViewer},
			}
		case leadwire.OpList:
			return leadwire.ListResult{Leads: []leadwire.Lead{{Role: "alpha", Inject: true}}}
		case leadwire.OpTicket:
			var p leadwire.TicketParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Error(err)
				return nil
			}
			if p.AttachRole == leadwire.AttachViewer {
				viewerCalls++
				return leadwire.Receipt{Op: req.Op, Role: p.Role, CommandID: p.CommandID, Status: leadwire.StatusInProgress}
			}
		}
		writerCalls++
		return nil
	})
	if err := l.Detect(); err != nil {
		t.Fatal(err)
	}
	list, err := l.List()
	want := LeadCapabilities{Directory: true, Viewer: true}
	if err != nil || len(list.Leads) != 1 || list.Leads[0].Capabilities != want || list.Leads[0].Inject {
		t.Fatalf("directory = %+v, %v", list, err)
	}
	if !list.Leads[0].MessagesEnabled {
		t.Error("missing lead.inject masked the lead's configured message switch")
	}
	for _, call := range []func() (LeadReceipt, error){
		func() (LeadReceipt, error) { return l.Ticket("alpha", 1, true) },
		func() (LeadReceipt, error) { return l.Define("alpha") },
		func() (LeadReceipt, error) { return l.Start("alpha") },
		func() (LeadReceipt, error) { return l.Stop("alpha", 1) },
		func() (LeadReceipt, error) { return l.Send("alpha", 1, "m1", "message") },
	} {
		if _, err := call(); !errors.Is(err, ErrLeadCapability) {
			t.Errorf("refusal = %v", err)
		}
	}
	if len(l.pending) != 0 {
		t.Errorf("kept command IDs for refused actions: %d", len(l.pending))
	}
	if r, err := l.Ticket("alpha", 1, false); err != nil || r.Status != LeadInProgress {
		t.Fatalf("viewer ticket = %+v, %v", r, err)
	}
	if writerCalls != 0 || viewerCalls != 1 {
		t.Fatalf("writer calls = %d, viewer calls = %d", writerCalls, viewerCalls)
	}
}

func TestPartialOpsCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ops   []string
		roles json.RawMessage
		want  LeadCapabilities
	}{
		{"directory only", []string{leadwire.OpList}, nil, LeadCapabilities{Directory: true}},
		{"conversation only", []string{"conversation.read"}, nil, LeadCapabilities{Conversation: true}},
		{"start only", []string{leadwire.OpStart}, nil, LeadCapabilities{Start: true}},
		{"partial ticket", []string{leadwire.OpTicket}, nil, LeadCapabilities{Viewer: true}},
		{"writer only", []string{leadwire.OpTicket}, json.RawMessage(`["writer"]`), LeadCapabilities{Writer: true}},
		{"roles need ticket", []string{leadwire.OpList}, json.RawMessage(`["writer"]`), LeadCapabilities{Directory: true}},
		{"unknown roles", []string{leadwire.OpList, leadwire.OpTicket}, json.RawMessage(`["unknown"]`), LeadCapabilities{Directory: true}},
		{"legacy scratch", leadwire.Operations(), nil, LeadCapabilities{Directory: true, Viewer: true, Writer: true, Define: true, Start: true, Stop: true, Inject: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := leadHelloResult{HelloResult: clientapi.HelloResult{Operations: tt.ops}, TicketRoles: tt.roles}
			if got, err := h.capabilities(); err != nil || got != tt.want {
				t.Errorf("capabilities = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestCapabilitiesEmptyTicketRoles(t *testing.T) {
	l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
		if req.Op != clientapi.OpHello {
			t.Fatalf("unexpected call %s", req.Op)
		}
		return map[string]any{"protocols": clientapi.SupportedProtocols(), "operations": []string{leadwire.OpTicket}, "ticket_roles": []string{}}
	})
	for _, writer := range []bool{false, true} {
		if _, err := l.Ticket("alpha", 1, writer); !errors.Is(err, ErrLeadCapability) {
			t.Errorf("writer = %t, refusal = %v", writer, err)
		}
	}
}

func TestCapabilitiesRefuseMalformedTicketRoles(t *testing.T) {
	for _, roles := range []any{true, nil} {
		l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
			if req.Op != clientapi.OpHello {
				t.Fatalf("sent %s after bad hello", req.Op)
			}
			return map[string]any{"protocols": clientapi.SupportedProtocols(), "operations": leadwire.Operations(), "ticket_roles": roles}
		})
		var u *Unavailable
		if _, err := l.List(); !errors.As(err, &u) || u.State != StateBadAnswer || l.conn != nil {
			t.Fatalf("ticket_roles = %v: %v, connection kept = %t", roles, err, l.conn != nil)
		}
	}
}

func TestCapabilitiesRequireUnambiguousOffer(t *testing.T) {
	for _, fields := range []string{
		`"OPERATIONS":["lead.start"]`,
		`"operations":[],"operations":["lead.start"]`,
		`"operations":["lead.ticket"],"ticket_roles":[],"TICKET_ROLES":["writer"]`,
	} {
		l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
			if req.Op != clientapi.OpHello {
				t.Fatalf("sent %s after ambiguous hello", req.Op)
			}
			return json.RawMessage(`{"protocols":["` + clientapi.Protocol + `"],` + fields + `}`)
		})
		var u *Unavailable
		if _, err := l.Capabilities(); !errors.As(err, &u) || u.State != StateBadAnswer || l.conn != nil {
			t.Fatalf("%s: got %v, connection kept = %t", fields, err, l.conn != nil)
		}
	}
}

func TestCapabilitiesOnReconnect(t *testing.T) {
	for _, offer := range []string{leadwire.OpList, clientapi.OpStatus} {
		t.Run(offer, func(t *testing.T) {
			var starts []leadwire.StartParams
			downgrade := true
			l := leadsWithOffers(t, func(n int, req clientapi.Request) any {
				switch req.Op {
				case clientapi.OpHello:
					ops := []string{leadwire.OpList, leadwire.OpStart}
					if n > 1 && downgrade {
						ops = []string{offer}
					}
					return clientapi.HelloResult{Protocols: clientapi.SupportedProtocols(), Operations: ops}
				case leadwire.OpList:
					// Drop the directory-only connection so List dials again.
					if n == 2 {
						return nil
					}
					return leadwire.ListResult{}
				case leadwire.OpStart:
					var p leadwire.StartParams
					if err := json.Unmarshal(req.Params, &p); err != nil {
						t.Fatal(err)
					}
					starts = append(starts, p)
					if n == 1 {
						return nil
					}
					if downgrade {
						t.Fatal("resent start without an offer")
					}
					return leadwire.Receipt{Op: req.Op, Role: p.Role, CommandID: p.CommandID, Status: leadwire.StatusRefused}
				default:
					t.Fatalf("unexpected call %s", req.Op)
					return nil
				}
			})
			for range 2 {
				if _, err := l.Start("alpha"); !errors.Is(err, ErrAnswerLost) {
					t.Fatalf("lost start = %v", err)
				}
			}
			if len(starts) != 1 || len(l.pending) != 1 {
				t.Fatalf("starts = %v, pending = %d", starts, len(l.pending))
			}
			downgrade = false
			if _, err := l.List(); err != nil {
				t.Fatal(err)
			}
			r, err := l.Start("alpha")
			if err != nil || !r.Earlier || len(starts) != 2 || starts[0] != starts[1] ||
				r.CommandID != starts[0].CommandID || r.CommandID == "" || len(l.pending) != 0 {
				t.Fatalf("recovered start = %+v, %v; sends = %+v, pending = %d", r, err, starts, len(l.pending))
			}
		})
	}
}

func TestCapabilitiesPerLeadMessageSwitch(t *testing.T) {
	res := leadwire.ListResult{Leads: []leadwire.Lead{{Role: "alpha", Inject: true}, {Role: "beta"}}}
	rows, ok := buildListing(res, LeadCapabilities{Directory: true, Conversation: true, Inject: true})
	if !ok || len(rows.Leads) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows.Leads {
		if row.Capabilities.Inject != (row.Role == "alpha") || row.Inject != row.Capabilities.Inject || !row.Capabilities.Conversation {
			t.Errorf("row = %+v", row)
		}
	}
}

// Omit each scratch operation in turn. The original all-or-nothing client
// refused all six cases. Only the absent operation should now be refused.
func TestPartialOpsEachMissingOperation(t *testing.T) {
	all := leadwire.Operations()
	for i, missing := range all {
		t.Run(missing, func(t *testing.T) {
			ops := append([]string(nil), all[:i]...)
			ops = append(ops, all[i+1:]...)
			var listCalls int
			l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
				switch req.Op {
				case clientapi.OpHello:
					return clientapi.HelloResult{Protocols: clientapi.SupportedProtocols(), Operations: ops}
				case leadwire.OpList:
					listCalls++
					return leadwire.ListResult{}
				default:
					t.Errorf("unexpected call %s", req.Op)
					return nil
				}
			})
			caps, err := l.Capabilities()
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range all {
				got := caps.Directory
				if op != leadwire.OpList {
					got = caps.CheckCommand(LeadOp(strings.TrimPrefix(op, "lead.")), false) == nil
				}
				if got != (op != missing) {
					t.Errorf("%s offered = %v", op, got)
				}
			}
			detectErr := l.Detect()
			_, listErr := l.List()
			if missing == leadwire.OpList {
				if !errors.Is(detectErr, ErrLeadsVersion) || !errors.Is(listErr, ErrLeadsVersion) || listCalls != 0 {
					t.Errorf("Detect = %v, List = %v, list calls = %d", detectErr, listErr, listCalls)
				}
				return
			}
			if detectErr != nil || listErr != nil || listCalls != 1 {
				t.Errorf("Detect = %v, List = %v, list calls = %d", detectErr, listErr, listCalls)
			}
		})
	}
}

func TestPartialOpsStartWithoutDirectory(t *testing.T) {
	l := leadsWithOffers(t, func(_ int, req clientapi.Request) any {
		switch req.Op {
		case clientapi.OpHello:
			return clientapi.HelloResult{Protocols: clientapi.SupportedProtocols(), Operations: []string{leadwire.OpStart}}
		case leadwire.OpStart:
			var params leadwire.StartParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				t.Error(err)
			}
			return leadwire.Receipt{CommandID: params.CommandID, Op: req.Op, Role: params.Role, Status: leadwire.StatusInProgress}
		}
		t.Errorf("unexpected call %s", req.Op)
		return nil
	})
	if !errors.Is(l.Detect(), ErrLeadsVersion) {
		t.Fatal("directory offered without lead.list")
	}
	if r, err := l.Start("alpha"); err != nil || r.Status != LeadInProgress {
		t.Fatalf("start = %+v, %v", r, err)
	}
}

func TestCapabilitiesRefusalAtCommand(t *testing.T) {
	caps := LeadCapabilities{Viewer: true}
	if err := caps.CheckCommand(LeadOpTicket, false); err != nil {
		t.Errorf("viewer = %v", err)
	}
	for _, tt := range []struct {
		op     LeadOp
		detail string
	}{
		{LeadOpTicket, "writer ticket"},
		{LeadOpStart, "starting"},
		{LeadOpStop, "stopping"},
		{LeadOpDefine, "defining"},
		{LeadOpInject, "sending a message"},
	} {
		err := caps.CheckCommand(tt.op, true)
		if !errors.Is(err, ErrLeadCapability) || !strings.Contains(err.Error(), tt.detail) {
			t.Errorf("%s refusal = %v", tt.op, err)
		}
	}
	if err := caps.CheckCommand("unknown", false); err != ErrLeadRequest {
		t.Errorf("unknown action = %v", err)
	}
}
