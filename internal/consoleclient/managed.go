package consoleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"

	"ataniq/internal/clientapi"
)

// Home is one managed home a view was read from.
type Home struct {
	// ID is [HomeID] of the home's authority.
	ID string
	// Directory is the target ID of the home's directory, kept whole.
	Directory string
	// Capabilities holds, for every target of the home that the home listed
	// (the directory and each lead), the operations it allows. A lead
	// listed with no operation can be seen and nothing more.
	Capabilities map[string][]string
	// LeftOut counts the rows of the home that are not in the view.
	LeftOut int
	// Offline tells that the last read of the home failed, and that the
	// rows of its projects are the last good read's.
	Offline bool
}

// HomeID is the bounded identifier of a home in a [View], computed from its
// whole authority string before any cleaning. A caller that names a home to
// refresh gives the authority it knows through this function.
func HomeID(authority string) string { return idOf("home", authority) }

// idOf hashes its parts, each with its length, so that moving a boundary
// between two parts gives another identifier.
func idOf(kind string, parts ...string) string {
	h := sha256.New()
	for _, p := range append([]string{kind}, parts...) {
		h.Write([]byte(strconv.Itoa(len(p)) + ":"))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// managedAttempts bounds the rounds of hello and list that [ReadManaged]
// makes when the home answers that the directory target it listed is stale.
const managedAttempts = 3

// ReadManaged reads one home's directory over conn, a stream that the caller
// opened and authenticated, and returns a view of its leads or a state that
// says why there is none. As [Read] does, it never returns an error. It
// closes conn before it returns.
//
// It speaks the managed protocol alone. A peer that does not speak it is
// [StateOtherProtocol]: there is no fallback to the version 1 protocol or to
// the local scratch inventory, and no request of version 1 is sent. It sends
// "hello" and the directory's read-only list, and nothing else: it never
// asks for an operation on a lead, whatever the capabilities say.
//
// Every identity of the view is made from the whole strings the home sent,
// before they are cleaned or cut for showing, so two rows that look alike are
// two rows. A home whose answers contradict each other (a row without a
// capability, a target that is not its fields', a row of another home) is
// [StateBadAnswer]. A principal that sees no directory gets a view with no
// home and no project. The context's deadline bounds the whole read.
func ReadManaged(ctx context.Context, conn net.Conn) Result {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	m := clientapi.NewManagedClient(conn)
	defer m.Close()
	for range managedAttempts {
		if res, again := readManagedOnce(ctx, m); !again {
			return res
		}
	}
	return state(StateUnavailable)
}

// managedCallState is [callState] for the managed protocol, where "forbidden"
// is the home's refusal of this principal.
func managedCallState(err error) State {
	var e *clientapi.Error
	if errors.As(err, &e) && e.Code == clientapi.CodeForbidden {
		return StateRefused
	}
	return callState(err)
}

// readManagedOnce makes one round, and reports true when the directory target
// was stale and a fresh hello is due.
func readManagedOnce(ctx context.Context, m *clientapi.ManagedClient) (Result, bool) {
	hello, err := m.Hello(ctx)
	if err != nil {
		return state(managedCallState(err)), false
	}
	if !slices.Contains(hello.Protocols, clientapi.ProtocolV2) {
		return state(StateOtherProtocol), false
	}
	if len(hello.Capabilities) == 0 {
		return Result{State: StateOK}, false
	}
	var dir string
	for target, ops := range hello.Capabilities {
		if !slices.Contains(ops, clientapi.OpDirectoryList) {
			continue
		}
		if dir != "" {
			return state(StateBadAnswer), false
		}
		dir = target
	}
	authority, ok := directoryAuthority(dir)
	if !ok {
		return state(StateBadAnswer), false
	}
	var list clientapi.DirectoryListResult
	if err := m.Call(ctx, dir, clientapi.OpDirectoryList, nil, &list); err != nil {
		var e *clientapi.Error
		if errors.As(err, &e) && e.Code == clientapi.CodeForbidden {
			// The directory changed after the hello: ask again from it.
			return Result{}, true
		}
		return state(managedCallState(err)), false
	}
	view, ok := buildManagedView(authority, dir, hello.Capabilities, list.Rows)
	if !ok {
		return state(StateBadAnswer), false
	}
	return Result{State: StateOK, View: view}, false
}

// directoryAuthority returns the authority in a directory target ID,
// "directory/AUTHORITY/DIGEST".
func directoryAuthority(target string) (string, bool) {
	rest, ok := strings.CutPrefix(target, "directory/")
	if !ok {
		return "", false
	}
	at := strings.LastIndexByte(rest, '/')
	if at <= 0 || at == len(rest)-1 {
		return "", false
	}
	return rest[:at], true
}

// buildManagedView turns the capabilities and rows of one home into a view.
// It reports false for an answer that contradicts itself.
func buildManagedView(authority, dir string, caps clientapi.TargetCapabilities, rows []clientapi.DirectoryRow) (View, bool) {
	if len(caps) != len(rows)+1 {
		return View{}, false
	}
	home := Home{ID: HomeID(authority), Directory: dir, Capabilities: map[string][]string{dir: slices.Clone(caps[dir])}}
	v := View{Homes: []Home{home}}
	seenTarget := map[string]bool{dir: true}
	seenProject := map[string]bool{}
	for _, row := range rows {
		ops, listed := caps[row.Target]
		if !listed || seenTarget[row.Target] || row.Target != row.DirectoryTarget.ID() || row.Authority != authority {
			return View{}, false
		}
		seenTarget[row.Target] = true
		// The epoch of a lead never launched is not known, and the two
		// facts must agree: an epoch is at least 1 exactly when launched.
		if row.Launched != (row.LaunchEpoch > 0) || seenProject[row.ProjectID] || len(v.Projects) >= MaxProjects {
			v.Homes[0].LeftOut++
			continue
		}
		seenProject[row.ProjectID] = true
		v.Homes[0].Capabilities[row.Target] = slices.Clone(ops)
		lead := Entry{
			ID:            idOf("lead", authority, row.ProjectID, row.LeadRole),
			Kind:          KindLead,
			Name:          clean(row.LeadRole),
			Project:       clean(row.ProjectID),
			State:         EntryUnknown,
			Epoch:         row.LaunchEpoch,
			NeverLaunched: !row.Launched,
			Target:        row.Target,
		}
		if row.ConversationAvailable {
			lead.ConversationID = idOf("conversation", authority, row.ConversationID)
		}
		v.Projects = append(v.Projects, Project{
			HomeID: home.ID, ID: idOf("project", authority, row.ProjectID), Name: clean(row.DisplayName),
			HasLead: true, Lead: lead,
		})
	}
	v.LeftOut = v.Homes[0].LeftOut
	return v, true
}

// WithHome returns v with the rows of one home taken from next, a result of
// [ReadManaged] for that home, whose [HomeID] is home. The other homes' rows
// stay as they are.
//
// When next is a view, the home's rows are replaced by the new ones (a home
// that lists none has none now) and the home is no longer offline. When it is
// not a view, or is the view of another home, the home's rows stay, marked
// Stale, and the home is in OfflineHomes: an unreachable home is shown as
// offline with what was last read, never as empty. A home not read before is
// offline with no rows. The result shares nothing with v.
func (v View) WithHome(home string, next Result) View {
	out := v
	out.Homes = make([]Home, len(v.Homes))
	for i, h := range v.Homes {
		out.Homes[i] = h
		out.Homes[i].Capabilities = cloneCaps(h.Capabilities)
	}
	at := slices.IndexFunc(out.Homes, func(h Home) bool { return h.ID == home })
	if at < 0 {
		out.Homes = append(out.Homes, Home{ID: home})
		at = len(out.Homes) - 1
	}
	fresh := next.State == StateOK && len(next.View.Homes) <= 1 &&
		(len(next.View.Homes) == 0 || next.View.Homes[0].ID == home)
	var kept []Project
	for _, p := range v.Projects {
		if p.HomeID != home {
			kept = append(kept, p)
		}
	}
	var mine []Project
	switch {
	case !fresh:
		for _, p := range v.Projects {
			if p.HomeID == home {
				p.Stale = true
				mine = append(mine, p)
			}
		}
		out.Homes[at].Offline = true
	case len(next.View.Homes) == 0:
		out.Homes = slices.Delete(out.Homes, at, at+1)
	default:
		out.Homes[at] = next.View.Homes[0]
		out.Homes[at].Capabilities = cloneCaps(next.View.Homes[0].Capabilities)
		mine = slices.Clone(next.View.Projects)
	}
	// Order: the homes in the order first read, a home's rows in its order.
	out.Projects = nil
	over := 0
	for _, h := range out.Homes {
		rows := mine
		if h.ID != home {
			rows = nil
			for _, p := range kept {
				if p.HomeID == h.ID {
					rows = append(rows, p)
				}
			}
		}
		for _, p := range rows {
			if len(out.Projects) >= MaxProjects {
				over++
				continue
			}
			out.Projects = append(out.Projects, p)
		}
	}
	// The rows over the bound of a view are counted for this view only.
	out.LeftOut, out.OfflineHomes = over, nil
	for _, h := range out.Homes {
		out.LeftOut += h.LeftOut
		if h.Offline {
			out.OfflineHomes = append(out.OfflineHomes, h.ID)
		}
	}
	return out
}

func cloneCaps(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, ops := range m {
		out[k] = slices.Clone(ops)
	}
	return out
}
