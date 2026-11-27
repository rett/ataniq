package protocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"

	core "ataniq/internal/agentsession/protocol"
)

const permissionMethod = "item/permissions/requestApproval"

var ErrGrant = errors.New("invalid or broader app-server permission grant")

// Grant describes exactly the returned permissions. Empty Permissions denies
// the request. Scope defaults to turn on decode, as in the pinned schema.
type Grant struct {
	Permissions      *Permissions `json:"permissions"`
	Scope            string       `json:"scope"`
	StrictAutoReview *bool        `json:"strictAutoReview,omitzero"`
}

type Permissions struct {
	FileSystem *FileSystem `json:"fileSystem,omitzero"`
	Network    *Network    `json:"network,omitzero"`
}

type Network struct {
	Enabled *bool `json:"enabled,omitzero"`
}

type FileSystem struct {
	Read             []string `json:"read,omitzero"`
	Write            []string `json:"write,omitzero"`
	Entries          []Entry  `json:"entries,omitzero"`
	GlobScanMaxDepth *uint64  `json:"globScanMaxDepth,omitzero"`
}

type Entry struct {
	Access string         `json:"access"`
	Path   jsontext.Value `json:"path"`
}

func (c *codec) permissionRequest(e envelope) (*Permissions, error) {
	if e.ID == nil || e.method != permissionMethod {
		return nil, ErrGrant
	}
	var p struct {
		Thread      string       `json:"threadId"`
		Turn        string       `json:"turnId"`
		Item        string       `json:"itemId"`
		Cwd         string       `json:"cwd"`
		Started     *int64       `json:"startedAtMs"`
		Reason      *string      `json:"reason"`
		Environment *string      `json:"environmentId"`
		Permissions *Permissions `json:"permissions"`
	}
	if json.Unmarshal(e.Params, &p, json.RejectUnknownMembers(true)) != nil ||
		p.Item == "" || p.Cwd == "" || p.Started == nil || !p.Permissions.valid() {
		return nil, ErrGrant
	}
	if !c.current(p.Thread, p.Turn) {
		return nil, ErrTurn
	}
	return p.Permissions, nil
}

// DecodeGrant binds a recorded answer to its server request (including the ID
// type) and to the currently observed turn. Never infer rights from the request.
func (c *codec) DecodeGrant(request, reply []byte) (Grant, error) {
	req, err := parse(request)
	if err != nil {
		return Grant{}, err
	}
	want, err := c.permissionRequest(req)
	if err != nil {
		return Grant{}, err
	}
	res, err := parse(reply)
	if err != nil {
		return Grant{}, err
	}
	if res.Result.Kind() != '{' || !sameID(req.ID, res.ID) {
		return Grant{}, ErrGrant
	}
	g := Grant{Scope: "turn"}
	if json.Unmarshal(res.Result, &g, json.RejectUnknownMembers(true)) != nil || !g.allowed(want) {
		return Grant{}, ErrGrant
	}
	return g, nil
}

func sameID(a, b jsontext.Value) bool {
	if a.Kind() == '"' && b.Kind() == '"' {
		left, _ := jsontext.AppendUnquote(nil, a)
		right, _ := jsontext.AppendUnquote(nil, b)
		return string(left) == string(right)
	}
	return string(a) == string(b)
}

// EncodeGrant emits only the selected profile. It applies the same checks
// again so a changed decision or a completed turn cannot issue a wider reply.
func (c *codec) EncodeGrant(request []byte, g Grant) ([]byte, error) {
	req, err := parse(request)
	if err != nil {
		return nil, err
	}
	want, err := c.permissionRequest(req)
	if err != nil {
		return nil, err
	}
	if !g.allowed(want) {
		return nil, ErrGrant
	}
	b, err := json.Marshal(map[string]any{"id": req.ID, "result": g}, json.Deterministic(true))
	if err != nil {
		return nil, ErrGrant
	}
	if len(b) >= core.MaxFrameBytes {
		return nil, core.ErrFrameTooLarge
	}
	return b, nil
}

func (p *Permissions) valid() bool {
	if p == nil {
		return false
	}
	f := p.FileSystem
	if f == nil {
		return true
	}
	// Mixing legacy lists with entries has no documented precedence.
	if f.Entries != nil && (f.Read != nil || f.Write != nil) ||
		f.GlobScanMaxDepth != nil && *f.GlobScanMaxDepth == 0 ||
		slices.Contains(f.Read, "") || slices.Contains(f.Write, "") {
		return false
	}
	for _, e := range f.Entries {
		if (e.Access != "read" && e.Access != "write" && e.Access != "deny") || !validPath(e.Path) {
			return false
		}
	}
	return true
}

// Validate the tagged path union before comparing it. Never resolve paths,
// expand a glob, or translate a special path into a local filesystem name.
func validPath(raw jsontext.Value) bool {
	var fields map[string]jsontext.Value
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	str := func(v jsontext.Value) string {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return s
	}
	if len(fields) != 2 {
		return false
	}
	switch str(fields["type"]) {
	case "path":
		return str(fields["path"]) != ""
	case "glob_pattern":
		return str(fields["pattern"]) != ""
	case "special":
		var value map[string]jsontext.Value
		if json.Unmarshal(fields["value"], &value) != nil {
			return false
		}
		switch str(value["kind"]) {
		case "root", "minimal", "tmpdir", "slash_tmp":
			return len(value) == 1
		case "unknown":
			if str(value["path"]) == "" {
				return false
			}
			delete(value, "path")
		case "project_roots":
		default:
			return false
		}
		sub := value["subpath"]
		return len(value) == 1 || len(value) == 2 && (sub.Kind() == 'n' || str(sub) != "")
	}
	return false
}

func (g Grant) allowed(request *Permissions) bool {
	if g.Scope != "turn" || !g.Permissions.valid() {
		return false
	}
	net := g.Permissions.Network
	if net != nil && net.Enabled != nil && *net.Enabled &&
		(request.Network == nil || request.Network.Enabled == nil || !*request.Network.Enabled) {
		return false
	}
	f, want := g.Permissions.FileSystem, request.FileSystem
	if f == nil {
		return true
	}
	if want == nil {
		return false
	}
	if !subset(f.Read, want.Read) || !subset(f.Write, want.Write) {
		return false
	}
	if (f.GlobScanMaxDepth == nil) != (want.GlobScanMaxDepth == nil) ||
		f.GlobScanMaxDepth != nil && *f.GlobScanMaxDepth != *want.GlobScanMaxDepth {
		return false
	}
	// A read rule may constrain a broader write rule. Without resolving
	// provider paths we cannot prove they are disjoint.
	hasWrite := slices.ContainsFunc(f.Entries, func(e Entry) bool { return e.Access == "write" })
	// Keep rule order and every deny. Treat an entry as indivisible.
	i := 0
	for _, requested := range want.Entries {
		if i < len(f.Entries) && f.Entries[i].Access == requested.Access && equalPath(f.Entries[i].Path, requested.Path) {
			i++
		} else if requested.Access == "deny" || hasWrite && requested.Access == "read" {
			return false
		}
	}
	return i == len(f.Entries)
}

func equalPath(a, b jsontext.Value) bool {
	// Validated path objects contain strings and null only. Canonical bytes
	// make object member order and JSON escapes immaterial.
	a, b = a.Clone(), b.Clone()
	return a.Canonicalize() == nil && b.Canonicalize() == nil && string(a) == string(b)
}

func subset(granted, requested []string) bool {
	count := make(map[string]int, len(requested))
	for _, p := range requested {
		count[p]++
	}
	for _, p := range granted {
		if count[p] == 0 {
			return false
		}
		count[p]--
	}
	return true
}
