package leadwire

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// strictDecode decodes data into v, refusing unknown members and trailing
// data, as the daemon decodes params.
func strictDecode(t *testing.T, data string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if dec.More() {
		t.Fatalf("trailing data after %s", data)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParamsJSON pins the exact JSON of every params struct: all keys, in
// order, with zero values present.
func TestParamsJSON(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"list", ListParams{}, `{}`},
		{"define", DefineParams{Client: "c1", CommandID: "id1", Role: "scratch-alpha/lead"},
			`{"client":"c1","command_id":"id1","role":"scratch-alpha/lead"}`},
		{"start", StartParams{Client: "c1", CommandID: "id2", Role: "scratch-alpha/lead"},
			`{"client":"c1","command_id":"id2","role":"scratch-alpha/lead"}`},
		{"stop", StopParams{Client: "c1", CommandID: "id3", Role: "scratch-alpha/lead", Epoch: 3},
			`{"client":"c1","command_id":"id3","role":"scratch-alpha/lead","epoch":3}`},
		{"stop current epoch", StopParams{Client: "c1", CommandID: "id3", Role: "scratch-alpha/lead"},
			`{"client":"c1","command_id":"id3","role":"scratch-alpha/lead","epoch":0}`},
		{"ticket", TicketParams{Client: "c1", CommandID: "id4", Role: "scratch-alpha/lead", Epoch: 3, AttachRole: AttachViewer},
			`{"client":"c1","command_id":"id4","role":"scratch-alpha/lead","epoch":3,"attach_role":"viewer"}`},
		{"inject", InjectParams{Client: "c1", CommandID: "id5", Role: "scratch-alpha/lead", Epoch: 3, MessageID: "m1", Body: "hello\nworld"},
			`{"client":"c1","command_id":"id5","role":"scratch-alpha/lead","epoch":3,"message_id":"m1","body":"hello\nworld"}`},
		{"zero define", DefineParams{}, `{"client":"","command_id":"","role":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, c.v); got != c.want {
				t.Errorf("marshal:\n got %s\nwant %s", got, c.want)
			}
			// Round trip through a strict decode into a fresh value.
			back := reflect.New(reflect.TypeOf(c.v))
			strictDecode(t, c.want, back.Interface())
			if !reflect.DeepEqual(back.Elem().Interface(), c.v) {
				t.Errorf("round trip: got %+v, want %+v", back.Elem().Interface(), c.v)
			}
		})
	}
}

// TestParamsRefuseUnknownMember checks that a strict decode of every params
// struct refuses an unknown member, so the structs hold every key of the wire.
func TestParamsRefuseUnknownMember(t *testing.T) {
	for _, v := range []any{&DefineParams{}, &StartParams{}, &StopParams{}, &TicketParams{}, &InjectParams{}} {
		dec := json.NewDecoder(strings.NewReader(`{"client":"c","command_id":"i","role":"r","extra":1}`))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err == nil {
			t.Errorf("%T accepted an unknown member", v)
		}
	}
}

// jsonKeys returns the JSON member names of a struct type, in field order.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("%s.%s has no json name", typ.Name(), typ.Field(i).Name)
		}
		keys = append(keys, name)
	}
	return keys
}

// TestParamsKeyOrder pins the order of the keys, which the digest follows.
func TestParamsKeyOrder(t *testing.T) {
	cases := []struct {
		v    any
		want []string
	}{
		{DefineParams{}, []string{"client", "command_id", "role"}},
		{StartParams{}, []string{"client", "command_id", "role"}},
		{StopParams{}, []string{"client", "command_id", "role", "epoch"}},
		{TicketParams{}, []string{"client", "command_id", "role", "epoch", "attach_role"}},
		{InjectParams{}, []string{"client", "command_id", "role", "epoch", "message_id", "body"}},
	}
	for _, c := range cases {
		if got := jsonKeys(t, c.v); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%T keys %v, want %v", c.v, got, c.want)
		}
	}
	// No params field may be omitted when empty: every key is required.
	for _, c := range cases {
		typ := reflect.TypeOf(c.v)
		for i := 0; i < typ.NumField(); i++ {
			if strings.Contains(typ.Field(i).Tag.Get("json"), "omitempty") {
				t.Errorf("%s.%s is omitempty", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

// TestResultJSON pins the exact JSON of each result struct.
func TestResultJSON(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"define", DefineResult{Role: "scratch-alpha/lead", State: "desired", ProfileDigest: "sha256:ab"},
			`{"role":"scratch-alpha/lead","state":"desired","profile_digest":"sha256:ab"}`},
		{"start", StartResult{Role: "scratch-alpha/lead", State: "ready", Epoch: 3, Unit: "ataniq-lead-scratch-alpha.service", Waiver: "assertion-10"},
			`{"role":"scratch-alpha/lead","state":"ready","epoch":3,"unit":"ataniq-lead-scratch-alpha.service","waiver":"assertion-10"}`},
		{"stop", StopResult{Role: "scratch-alpha/lead", State: "stopped", Proof: "unit-gone"},
			`{"role":"scratch-alpha/lead","state":"stopped","proof":"unit-gone"}`},
		{"ticket", TicketResult{Role: "scratch-alpha/lead", Epoch: 3, TicketFile: "/srv/runtime/leads/scratch-alpha-lead/ticket", Folder: "/srv/runtime/leads/scratch-alpha-lead"},
			`{"role":"scratch-alpha/lead","epoch":3,"ticket_file":"/srv/runtime/leads/scratch-alpha-lead/ticket","folder":"/srv/runtime/leads/scratch-alpha-lead"}`},
		{"inject submitted", InjectResult{Role: "scratch-alpha/lead", Epoch: 3, MessageID: "m1", Result: Delivery{Outcome: "submitted", Written: "whole"}},
			`{"role":"scratch-alpha/lead","epoch":3,"message_id":"m1","result":{"outcome":"submitted","written":"whole"}}`},
		{"inject uncertain", InjectResult{Role: "scratch-alpha/lead", Epoch: 3, MessageID: "m1", Result: Delivery{Outcome: "uncertain", Reason: "died", Written: "unknown", Detail: "holder gone"}},
			`{"role":"scratch-alpha/lead","epoch":3,"message_id":"m1","result":{"outcome":"uncertain","reason":"died","written":"unknown","detail":"holder gone"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, c.v); got != c.want {
				t.Errorf("marshal:\n got %s\nwant %s", got, c.want)
			}
			back := reflect.New(reflect.TypeOf(c.v))
			strictDecode(t, c.want, back.Interface())
			if !reflect.DeepEqual(back.Elem().Interface(), c.v) {
				t.Errorf("round trip: got %+v, want %+v", back.Elem().Interface(), c.v)
			}
		})
	}
}

var (
	t0 = time.Date(2026, 11, 20, 12, 0, 0, 123456789, time.UTC)
	t1 = t0.Add(1500 * time.Millisecond)
)

func TestReceiptJSON(t *testing.T) {
	startResult := json.RawMessage(`{"role":"scratch-alpha/lead","state":"ready","epoch":3,"unit":"ataniq-lead-scratch-alpha.service","waiver":"assertion-10"}`)
	cases := []struct {
		name string
		r    Receipt
		want string
	}{
		{"in progress", Receipt{CommandID: "id2", Op: OpStart, Role: "scratch-alpha/lead", Status: StatusInProgress, AdmittedAt: t0},
			`{"command_id":"id2","op":"lead.start","role":"scratch-alpha/lead","status":"in-progress","admitted_at":"2026-11-20T12:00:00.123456789Z"}`},
		{"done", Receipt{CommandID: "id2", Op: OpStart, Role: "scratch-alpha/lead", Status: StatusDone, Result: startResult, AdmittedAt: t0, FinishedAt: &t1},
			`{"command_id":"id2","op":"lead.start","role":"scratch-alpha/lead","status":"done","result":` + string(startResult) + `,"admitted_at":"2026-11-20T12:00:00.123456789Z","finished_at":"2026-11-20T12:00:01.623456789Z"}`},
		{"refused", Receipt{CommandID: "id6", Op: OpStart, Role: "scratch-beta/lead", Status: StatusRefused, Code: CodeMaxRunning, Message: "the daemon is at its running limit", AdmittedAt: t0, FinishedAt: &t1},
			`{"command_id":"id6","op":"lead.start","role":"scratch-beta/lead","status":"refused","code":"max-running","message":"the daemon is at its running limit","admitted_at":"2026-11-20T12:00:00.123456789Z","finished_at":"2026-11-20T12:00:01.623456789Z"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, c.r); got != c.want {
				t.Errorf("marshal:\n got %s\nwant %s", got, c.want)
			}
			var back Receipt
			strictDecode(t, c.want, &back)
			if got := marshal(t, back); got != c.want {
				t.Errorf("round trip:\n got %s\nwant %s", got, c.want)
			}
			if !back.AdmittedAt.Equal(c.r.AdmittedAt) {
				t.Errorf("admitted_at %v, want %v", back.AdmittedAt, c.r.AdmittedAt)
			}
			if (back.FinishedAt == nil) != (c.r.FinishedAt == nil) {
				t.Errorf("finished_at presence differs")
			}
		})
	}
}

// TestReceiptResultDecodes checks that the result of a done receipt decodes
// into the operation's result struct.
func TestReceiptResultDecodes(t *testing.T) {
	want := InjectResult{Role: "scratch-alpha/lead", Epoch: 4, MessageID: "m9",
		Result: Delivery{Outcome: "submitted", Written: "whole"}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	line := marshal(t, Receipt{CommandID: "id", Op: OpInject, Role: want.Role, Status: StatusDone, Result: raw, AdmittedAt: t0, FinishedAt: &t1})
	var r Receipt
	strictDecode(t, line, &r)
	var got InjectResult
	strictDecode(t, string(r.Result), &got)
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestListJSON(t *testing.T) {
	last := Receipt{CommandID: "id2", Op: OpStart, Role: "scratch-alpha/lead", Status: StatusDone, AdmittedAt: t0, FinishedAt: &t1}
	l := ListResult{MaxRunning: 2, Running: 1, Leads: []Lead{
		{Key: "alpha", Role: "scratch-alpha/lead", Defined: true, Provision: ProvisionDone, State: "ready", Epoch: 3,
			Unit: "ataniq-lead-scratch-alpha.service", Inject: true, MessageSpent: false, Last: &last},
		{Key: "beta", Role: "scratch-beta/lead", Provision: ProvisionNone, State: ""},
	}}
	const want = `{"max_running":2,"running":1,"leads":[` +
		`{"key":"alpha","role":"scratch-alpha/lead","defined":true,"provision":"done","state":"ready","epoch":3,"unit":"ataniq-lead-scratch-alpha.service","inject":true,"message_spent":false,` +
		`"last":{"command_id":"id2","op":"lead.start","role":"scratch-alpha/lead","status":"done","admitted_at":"2026-11-20T12:00:00.123456789Z","finished_at":"2026-11-20T12:00:01.623456789Z"}},` +
		`{"key":"beta","role":"scratch-beta/lead","defined":false,"provision":"none","state":"","epoch":0,"unit":"","inject":false,"message_spent":false,"last":null}]}`
	if got := marshal(t, l); got != want {
		t.Errorf("marshal:\n got %s\nwant %s", got, want)
	}
	var back ListResult
	strictDecode(t, want, &back)
	if got := marshal(t, back); got != want {
		t.Errorf("round trip:\n got %s\nwant %s", got, want)
	}
	if back.Leads[1].Last != nil {
		t.Error("a null last decoded to a non-nil receipt")
	}
}

// TestEmptyListJSON checks that a daemon with no lead row answers an empty
// array, which a nil slice would not.
func TestEmptyListJSON(t *testing.T) {
	got := marshal(t, ListResult{MaxRunning: 2, Leads: []Lead{}})
	const want = `{"max_running":2,"running":0,"leads":[]}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestOperations(t *testing.T) {
	want := []string{"lead.list", "lead.define", "lead.start", "lead.stop", "lead.ticket", "lead.inject"}
	got := Operations()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Operations() = %v, want %v", got, want)
	}
	got[0] = "x"
	if Operations()[0] != "lead.list" {
		t.Error("Operations returned a shared slice")
	}
	for _, op := range want {
		// The client protocol's rule: lower-case letters, digits and dashes
		// around exactly one dot.
		before, after, ok := strings.Cut(op, ".")
		if !ok || before == "" || after == "" || strings.Contains(after, ".") {
			t.Errorf("%q is not a two-part operation name", op)
		}
		if strings.ToLower(op) != op {
			t.Errorf("%q is not lower case", op)
		}
		if Mutating(op) != (op != OpList) {
			t.Errorf("Mutating(%q) = %v", op, Mutating(op))
		}
	}
	if Mutating("lead.other") || Mutating("") {
		t.Error("Mutating accepted an unknown operation")
	}
}

func TestStatusFinal(t *testing.T) {
	for st, want := range map[Status]bool{
		StatusInProgress:  false,
		StatusDone:        true,
		StatusRefused:     true,
		StatusUnavailable: true,
		StatusUncertain:   true,
		"":                false,
		"other":           false,
	} {
		if got := st.Final(); got != want {
			t.Errorf("%q.Final() = %v, want %v", st, got, want)
		}
	}
	// The words of the design.
	words := map[Status]string{
		StatusInProgress: "in-progress", StatusDone: "done", StatusRefused: "refused",
		StatusUnavailable: "unavailable", StatusUncertain: "uncertain",
	}
	for st, w := range words {
		if string(st) != w {
			t.Errorf("status %q, want %q", st, w)
		}
	}
}

func TestConstants(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{ErrCodeInvalidRequest, "invalid-request"}, {ErrCodeConflict, "conflict"},
		{ErrCodeUnavailable, "unavailable"}, {ErrCodeRefused, "refused"},
		{CodeMaxRunning, "max-running"}, {CodeSecondMessage, "second-message"},
		{CodeMessageInFlight, "message-in-flight"}, {CodeStaleEpoch, "stale-epoch"},
		{AttachWriter, "writer"}, {AttachViewer, "viewer"},
		{ProvisionDone, "done"}, {ProvisionIncomplete, "incomplete"}, {ProvisionNone, "none"},
	} {
		if c.got != c.want {
			t.Errorf("constant %q, want %q", c.got, c.want)
		}
	}
}

// repeat returns n bytes of the letter c.
func repeat(c byte, n int) string { return string(bytes.Repeat([]byte{c}, n)) }

// maxInjectReceipt is the largest inject receipt: the longest role, message
// id, text and detail, and a timestamp with an offset.
func maxInjectReceipt(t *testing.T) Receipt {
	t.Helper()
	role := "scratch-" + repeat('k', MaxKey) + "/lead"
	res, err := json.Marshal(InjectResult{
		Role: role, Epoch: 1<<64 - 1, MessageID: repeat('m', MaxMessageID),
		Result: Delivery{Outcome: "uncertain", Reason: "not-submitted", Written: "partial", Detail: repeat('d', MaxInjectDetail)},
	})
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("", 14*3600+30*60)
	adm := time.Date(2026, 11, 20, 23, 59, 59, 999999999, zone)
	fin := adm.Add(time.Second)
	return Receipt{
		CommandID: repeat('i', MaxCommandID), Op: OpInject, Role: role,
		Status: StatusUncertain, Code: repeat('c', 32), Message: repeat('x', MaxMessage),
		Result: res, AdmittedAt: adm, FinishedAt: &fin,
	}
}

// TestLargestReceiptSize checks that the largest inject receipt stays under
// the approval frame's 8 KiB, so a later increment can relay it, and under
// the line of the command table.
func TestLargestReceiptSize(t *testing.T) {
	b, err := json.Marshal(maxInjectReceipt(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("largest inject receipt: %d bytes", len(b))
	if len(b) >= MaxRelayBytes {
		t.Errorf("largest receipt is %d bytes, want under %d", len(b), MaxRelayBytes)
	}
	if len(b) > MaxReceiptLine {
		t.Errorf("largest receipt is %d bytes, over the table's line of %d", len(b), MaxReceiptLine)
	}
}

// TestLargestListSize checks that lead.list with the most leads, each with a
// full-size latest receipt, stays under 8 KiB. The receipt in a row is the
// summary form: it carries no result (see [Lead.Last]), because a row holds
// the lead's state, epoch and unit already and a result is at most 1.4 KiB.
// TestListWithResultsExceedsRelay records what the size would be with results.
func TestLargestListSize(t *testing.T) {
	b := marshal(t, maxList(t, false))
	t.Logf("largest lead.list: %d bytes", len(b))
	if len(b) >= MaxRelayBytes {
		t.Errorf("largest lead.list is %d bytes, want under %d", len(b), MaxRelayBytes)
	}
}

// TestListWithResultsExceedsRelay documents why a row's receipt carries no
// result: eight rows with full inject results do not fit in 8 KiB. If this
// test fails, the limit changed and the summary rule can be reconsidered.
func TestListWithResultsExceedsRelay(t *testing.T) {
	b := marshal(t, maxList(t, true))
	t.Logf("lead.list with a full result in every row: %d bytes", len(b))
	if len(b) < MaxRelayBytes {
		t.Errorf("lead.list with results is %d bytes, under %d: the summary rule is not needed", len(b), MaxRelayBytes)
	}
}

// maxList is the largest lead.list: MaxLeads rows with the longest values.
func maxList(t *testing.T, withResult bool) ListResult {
	t.Helper()
	key := repeat('k', MaxKey)
	role := "scratch-" + key + "/lead"
	l := ListResult{MaxRunning: MaxLeads, Running: MaxLeads}
	for i := 0; i < MaxLeads; i++ {
		r := maxInjectReceipt(t)
		if !withResult {
			r.Result = nil
		}
		l.Leads = append(l.Leads, Lead{
			Key: key, Role: role, Defined: true, Provision: ProvisionIncomplete,
			State: "degraded", Epoch: 1<<64 - 1,
			Unit:   "ataniq-lead-scratch-" + key + ".service",
			Inject: true, MessageSpent: true, Last: &r,
		})
	}
	return l
}

// TestCodeListsHoldEveryCodeConstant reads ops.go and holds that ErrorCodes
// and ReasonCodes hold, in order, the value of every constant named ErrCode*
// and Code*, so a code added later is added to its list too.
func TestCodeListsHoldEveryCodeConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ops.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var errCodes, reasons []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasPrefix(n.Name, "ErrCode"):
					errCodes = append(errCodes, v)
				case strings.HasPrefix(n.Name, "Code"):
					reasons = append(reasons, v)
				}
			}
		}
	}
	if len(errCodes) == 0 || len(reasons) == 0 {
		t.Fatalf("read %d error codes and %d reason codes: ops.go was not read", len(errCodes), len(reasons))
	}
	if got := ErrorCodes(); !reflect.DeepEqual(got, errCodes) {
		t.Errorf("ErrorCodes() = %q, the constants are %q", got, errCodes)
	}
	if got := ReasonCodes(); !reflect.DeepEqual(got, reasons) {
		t.Errorf("ReasonCodes() = %q, the constants are %q", got, reasons)
	}
	// The lists are new slices.
	ErrorCodes()[0], ReasonCodes()[0] = "x", "x"
	if ErrorCodes()[0] == "x" || ReasonCodes()[0] == "x" {
		t.Error("a list is shared between calls")
	}
}
