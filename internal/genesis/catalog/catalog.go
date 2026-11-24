package catalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

// Schema is the schema name of a catalog.
const Schema = "ataniq.genesis-catalog/1"

// The caps and bounds of a catalog.
const (
	// MaxCatalogBytes is the cap of a catalog's data.
	MaxCatalogBytes = 65536
	// MaxNodes is the most nodes a catalog holds.
	MaxNodes = 64
	// MaxPrompt is the cap of a prompt, in bytes.
	MaxPrompt = 160
	// MaxLabel is the cap of an option's label, in bytes.
	MaxLabel = 60
	// MaxExample is the cap of an option's one-line example, in bytes.
	MaxExample = 100
	// MinOptions and MaxOptions bound the options of a single or multiple
	// choice.
	MinOptions, MaxOptions = 2, 8
	// MaxWeight is the highest weight of a node.
	MaxWeight = 1000
	// MaxInvalidates is the most fields one node invalidates.
	MaxInvalidates = 32
	// maxDepth is how deep a catalog's JSON nests.
	maxDepth = 16
)

// Type is the type of a question.
type Type string

// The types of a question.
const (
	// Single is one option among 2 to 8.
	Single Type = "single"
	// Multi is one or more options among 2 to 8.
	Multi Type = "multi"
	// YesNo is the two options yes and no.
	YesNo Type = "yesno"
	// Scale is a step from 1 to 5; it has no options.
	Scale Type = "scale"
)

// The reserved answer words: no option has one of them as its id.
const (
	// AnswerOther is free text given instead of an option.
	AnswerOther = "other"
	// AnswerYouDecide leaves the choice to the host.
	AnswerYouDecide = "you_decide"
	// AnswerSkip leaves the question to its default.
	AnswerSkip = "skip"
)

// Option is one choice of a question.
type Option struct {
	// ID is the choice id written into the brief.
	ID string
	// Label is the option's short text.
	Label string
	// Example is a one-line example of the option.
	Example string
}

// Node is one question of the catalog.
type Node struct {
	// ID is the node's id, recorded in the field it sets.
	ID string
	// Field is the brief field the answer sets.
	Field string
	// Type is the type of the question.
	Type Type
	// Topic is the node's own topic label. It does not group a round: a
	// round may hold nodes of several topics.
	Topic string
	// Prompt is the question as the owner reads it.
	Prompt string
	// Options are the choices: 2 to 8 for a single or multiple choice,
	// yes and no for a yes-or-no question, none for a scale.
	Options []Option
	// AllowOther allows free text instead of an option.
	AllowOther bool
	// AllowYouDecide allows leaving the choice to the host, which sets
	// the default with the provenance YouDecide names.
	AllowYouDecide bool
	// YouDecide is the provenance of a choice left to the host: default
	// or supervisor-inferred. It is set exactly when AllowYouDecide is.
	YouDecide brief.Provenance
	// Default is the choice a skip, or a choice left to the host, sets.
	// A node without one cannot be skipped.
	Default string
	// When is the node's condition; nil always holds.
	When *Predicate
	// Invalidates are the fields cleared when this node's answer changes.
	Invalidates []string
	// Weight orders the nodes: the highest first.
	Weight int
	// RoundHint is the round the node is meant for, 1 to 6.
	RoundHint int
}

// Catalog is a loaded, valid question catalog. Build one with [Load] or
// [V1]; the zero Catalog is refused by the walker.
type Catalog struct {
	// Version is the catalog's version id, such as v1.
	Version string
	// Nodes are the questions.
	Nodes []Node
	// Required lists the required fields of each kind's brief schema.
	Required map[string][]string

	digest  string
	index   map[string]int
	writers map[string][]int
	closure map[string][]string
}

// Digest returns the SHA-256 of the catalog's canonical form in lower-case
// hexadecimal. A brief started under this catalog records it.
func (c Catalog) Digest() string { return c.digest }

// Node returns the node with the id.
func (c Catalog) Node(id string) (Node, bool) {
	i, ok := c.index[id]
	if !ok {
		return Node{}, false
	}
	return c.Nodes[i], true
}

// Closure returns the fields cleared when an answer to a node setting the
// field changes: the fields the node invalidates, and in turn the fields
// those invalidate, sorted.
func (c Catalog) Closure(field string) []string {
	return append([]string(nil), c.closure[field]...)
}

//go:embed catalog_v1.json
var v1Data []byte

var v1 = sync.OnceValues(func() (Catalog, error) { return Load(v1Data) })

// V1 returns the embedded catalog of version v1. Each call returns its own
// copy of the nodes and the schemas, so a caller that changes them changes
// no other caller's catalog.
func V1() (Catalog, error) {
	c, err := v1()
	if err != nil {
		return Catalog{}, err
	}
	return c.clone(), nil
}

// clone returns a copy of c whose nodes and schemas share nothing with c.
// The indexes are never changed after Load, and are shared.
func (c Catalog) clone() Catalog {
	nodes := make([]Node, len(c.Nodes))
	for i, n := range c.Nodes {
		n.Options = append([]Option(nil), n.Options...)
		n.Invalidates = append([]string{}, n.Invalidates...)
		if n.When != nil {
			w := n.When.clone()
			n.When = &w
		}
		nodes[i] = n
	}
	c.Nodes = nodes
	req := make(map[string][]string, len(c.Required))
	for k, v := range c.Required {
		req[k] = append([]string(nil), v...)
	}
	c.Required = req
	return c
}

// kinds are the kinds a catalog lists required fields for.
var kinds = []string{brief.DomainMedia, brief.DomainOther, brief.DomainPublishing, brief.DomainSoftware}

func invalid(detail string) error {
	return &brief.Error{Code: brief.CodeCatalogInvalid, Detail: detail}
}

// Load reads a catalog strictly and checks it: its form, its texts, its
// conditions, its invalidations and its brief schemas. A refusal has the
// code catalog_invalid, or too_large for data over the cap.
func Load(data []byte) (Catalog, error) {
	if len(data) > MaxCatalogBytes {
		return Catalog{}, &brief.Error{Code: brief.CodeTooLarge, Detail: "the catalog is larger than 65536 bytes"}
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return Catalog{}, invalid(err.Error())
	}
	c, err := decode(v)
	if err != nil {
		return Catalog{}, err
	}
	if err := c.check(); err != nil {
		return Catalog{}, err
	}
	out, err := Encode(c)
	if err != nil {
		return Catalog{}, err
	}
	sum := sha256.Sum256(out)
	c.digest = hex.EncodeToString(sum[:])
	return c, nil
}

// Encode writes the catalog in the canonical form.
func Encode(c Catalog) ([]byte, error) {
	nodes := make([]canon.Value, len(c.Nodes))
	for i, n := range c.Nodes {
		opts := make([]canon.Value, len(n.Options))
		for j, o := range n.Options {
			opts[j] = canon.Obj(
				canon.M("id", canon.Str(o.ID)),
				canon.M("label", canon.Str(o.Label)),
				canon.M("example", canon.Str(o.Example)),
			)
		}
		ms := []canon.Member{
			canon.M("id", canon.Str(n.ID)),
			canon.M("field", canon.Str(n.Field)),
			canon.M("type", canon.Str(string(n.Type))),
			canon.M("topic", canon.Str(n.Topic)),
			canon.M("prompt", canon.Str(n.Prompt)),
			canon.M("options", canon.List(opts...)),
			canon.M("allow_other", canon.Boolean(n.AllowOther)),
			canon.M("allow_you_decide", canon.Boolean(n.AllowYouDecide)),
			canon.M("invalidates", canon.Strs(n.Invalidates)),
			canon.M("weight", canon.Num(int64(n.Weight))),
			canon.M("round_hint", canon.Num(int64(n.RoundHint))),
		}
		if n.YouDecide != "" {
			ms = append(ms, canon.M("you_decide", canon.Str(string(n.YouDecide))))
		}
		if n.Default != "" {
			ms = append(ms, canon.M("default", canon.Str(n.Default)))
		}
		if n.When != nil {
			ms = append(ms, canon.M("when", n.When.encode()))
		}
		nodes[i] = canon.Obj(ms...)
	}
	req := make([]canon.Member, 0, len(c.Required))
	for k, fs := range c.Required {
		req = append(req, canon.M(k, canon.Strs(fs)))
	}
	out, err := canon.Encode(canon.Obj(
		canon.M("schema", canon.Str(Schema)),
		canon.M("version", canon.Str(c.Version)),
		canon.M("nodes", canon.List(nodes...)),
		canon.M("required", canon.Obj(req...)),
	))
	if err != nil {
		return nil, invalid("the catalog does not encode: " + err.Error())
	}
	return out, nil
}

func decode(v canon.Value) (Catalog, error) {
	m, err := canon.Fields(v, "catalog", []string{"schema", "version", "nodes", "required"}, nil)
	if err != nil {
		return Catalog{}, invalid(err.Error())
	}
	schema, err := canon.AsString(m["schema"], "catalog.schema")
	if err != nil {
		return Catalog{}, invalid(err.Error())
	}
	if schema != Schema {
		return Catalog{}, invalid("catalog.schema is not " + Schema)
	}
	var c Catalog
	if c.Version, err = canon.AsString(m["version"], "catalog.version"); err != nil {
		return Catalog{}, invalid(err.Error())
	}
	nv := m["nodes"]
	if nv.Kind != canon.Array {
		return Catalog{}, invalid("catalog.nodes is not a list")
	}
	if len(nv.Arr) == 0 || len(nv.Arr) > MaxNodes {
		return Catalog{}, invalid("catalog.nodes does not hold 1 to 64 nodes")
	}
	c.Nodes = make([]Node, len(nv.Arr))
	for i, e := range nv.Arr {
		if c.Nodes[i], err = decodeNode(e, "catalog.nodes["+strconv.Itoa(i)+"]"); err != nil {
			return Catalog{}, err
		}
	}
	rv, err := canon.Fields(m["required"], "catalog.required", kinds, nil)
	if err != nil {
		return Catalog{}, invalid(err.Error())
	}
	c.Required = make(map[string][]string, len(kinds))
	for _, k := range kinds {
		if c.Required[k], err = canon.AsStrings(rv[k], "catalog.required."+k, MaxNodes); err != nil {
			return Catalog{}, invalid(err.Error())
		}
	}
	return c, nil
}

func decodeNode(v canon.Value, where string) (Node, error) {
	m, err := canon.Fields(v, where,
		[]string{"id", "field", "type", "topic", "prompt", "options", "allow_other", "allow_you_decide", "invalidates", "weight", "round_hint"},
		[]string{"you_decide", "default", "when"})
	if err != nil {
		return Node{}, invalid(err.Error())
	}
	var n Node
	str := func(key string, dst *string) {
		if err == nil {
			*dst, err = canon.AsString(m[key], where+"."+key)
		}
	}
	var typ, youDecide string
	str("id", &n.ID)
	str("field", &n.Field)
	str("type", &typ)
	str("topic", &n.Topic)
	str("prompt", &n.Prompt)
	if _, ok := m["you_decide"]; ok {
		str("you_decide", &youDecide)
		if err == nil && youDecide == "" {
			return Node{}, invalid(where + ".you_decide is empty; an empty optional field is left out")
		}
	}
	if _, ok := m["default"]; ok {
		str("default", &n.Default)
		if err == nil && n.Default == "" {
			return Node{}, invalid(where + ".default is empty; an empty optional field is left out")
		}
	}
	if err != nil {
		return Node{}, invalid(err.Error())
	}
	n.Type, n.YouDecide = Type(typ), brief.Provenance(youDecide)
	if n.AllowOther, err = canon.AsBool(m["allow_other"], where+".allow_other"); err != nil {
		return Node{}, invalid(err.Error())
	}
	if n.AllowYouDecide, err = canon.AsBool(m["allow_you_decide"], where+".allow_you_decide"); err != nil {
		return Node{}, invalid(err.Error())
	}
	if n.Invalidates, err = canon.AsStrings(m["invalidates"], where+".invalidates", MaxInvalidates); err != nil {
		return Node{}, invalid(err.Error())
	}
	if n.Weight, err = canon.AsInt(m["weight"], where+".weight", 1, MaxWeight); err != nil {
		return Node{}, invalid(err.Error())
	}
	if n.RoundHint, err = canon.AsInt(m["round_hint"], where+".round_hint", 1, MaxRounds); err != nil {
		return Node{}, invalid(err.Error())
	}
	ov := m["options"]
	if ov.Kind != canon.Array {
		return Node{}, invalid(where + ".options is not a list")
	}
	if len(ov.Arr) > MaxOptions {
		return Node{}, invalid(where + ".options holds more than 8 options")
	}
	n.Options = make([]Option, len(ov.Arr))
	for i, e := range ov.Arr {
		ow := where + ".options[" + strconv.Itoa(i) + "]"
		om, err := canon.Fields(e, ow, []string{"id", "label", "example"}, nil)
		if err != nil {
			return Node{}, invalid(err.Error())
		}
		o := &n.Options[i]
		for key, dst := range map[string]*string{"id": &o.ID, "label": &o.Label, "example": &o.Example} {
			if *dst, err = canon.AsString(om[key], ow+"."+key); err != nil {
				return Node{}, invalid(err.Error())
			}
		}
	}
	if wv, ok := m["when"]; ok {
		p, err := decodePredicate(wv, where+".when", 1)
		if err != nil {
			return Node{}, err
		}
		n.When = &p
	}
	return n, nil
}

// check holds every rule of a catalog, and builds its indexes.
func (c *Catalog) check() error {
	if !validVersion(c.Version) {
		return invalid("catalog.version is not a version id")
	}
	if len(c.Nodes) == 0 || len(c.Nodes) > MaxNodes {
		return invalid("catalog.nodes does not hold 1 to 64 nodes")
	}
	c.index = make(map[string]int, len(c.Nodes))
	c.writers = make(map[string][]int)
	for i := range c.Nodes {
		n := &c.Nodes[i]
		where := "catalog.nodes[" + strconv.Itoa(i) + "]"
		if err := n.check(where); err != nil {
			return err
		}
		if _, dup := c.index[n.ID]; dup {
			return invalid(where + ".id repeats the id of another node")
		}
		c.index[n.ID] = i
		c.writers[n.Field] = append(c.writers[n.Field], i)
	}
	// Conditions: every field a condition names is set by some node asked
	// in an earlier round, and every value it compares with is one such a
	// node can set.
	for i, n := range c.Nodes {
		if n.When == nil {
			continue
		}
		where := "catalog.nodes[" + strconv.Itoa(i) + "].when"
		for _, f := range n.When.fields(nil) {
			ws := c.writers[f]
			if len(ws) == 0 {
				return invalid(where + " names a field no node sets")
			}
			if f == n.Field {
				return invalid(where + " names the node's own field")
			}
			earlier := false
			for _, w := range ws {
				if c.Nodes[w].RoundHint < n.RoundHint {
					earlier = true
				}
			}
			if !earlier {
				return invalid(where + " names a field asked only in the same or a later round")
			}
		}
		ok := n.When.values(func(field, value string) bool {
			for _, w := range c.writers[field] {
				if c.Nodes[w].canSet(value) {
					return true
				}
			}
			return false
		})
		if !ok {
			return invalid(where + " compares with a value no node can set")
		}
	}
	// Invalidations: every field named is set by some node, and the
	// graph from a field to the fields its nodes invalidate has no cycle.
	graph := make(map[string][]string)
	for i, n := range c.Nodes {
		where := "catalog.nodes[" + strconv.Itoa(i) + "].invalidates"
		for _, f := range n.Invalidates {
			if len(c.writers[f]) == 0 {
				return invalid(where + " names a field no node sets")
			}
			graph[n.Field] = appendNew(graph[n.Field], f)
		}
	}
	if err := checkAcyclic(graph); err != nil {
		return err
	}
	c.closure = make(map[string][]string, len(c.writers))
	for f := range c.writers {
		c.closure[f] = closureOf(graph, f)
	}
	// A changed answer leaves no stale answer behind: a node whose
	// condition names a field is in the closure of that field.
	for i, n := range c.Nodes {
		if n.When == nil {
			continue
		}
		for _, f := range n.When.fields(nil) {
			if !contains(c.closure[f], n.Field) {
				return invalid("catalog.nodes[" + strconv.Itoa(i) + "].when names a field whose change does not invalidate the node's field")
			}
		}
	}
	// The kind decides the brief's domain, so a change of kind clears
	// every domain extension field.
	if len(c.writers[brief.FieldKind]) == 0 {
		return invalid("no node sets the kind")
	}
	for f := range c.writers {
		if brief.FieldDomain(f) != "" && !contains(c.closure[brief.FieldKind], f) {
			return invalid("a change of kind does not invalidate every domain extension field")
		}
	}
	// The brief schemas.
	for _, k := range kinds {
		where := "catalog.required." + k
		req := c.Required[k]
		if !contains(req, brief.FieldKind) {
			return invalid(where + " does not hold the kind")
		}
		for j, f := range req {
			if len(c.writers[f]) == 0 {
				return invalid(where + " names a field no node sets")
			}
			if d := brief.FieldDomain(f); d != "" && d != k {
				return invalid(where + " names an extension field of another kind")
			}
			if contains(req[:j], f) {
				return invalid(where + " names a field twice")
			}
		}
	}
	return nil
}

func (n Node) check(where string) error {
	switch {
	case !brief.ValidNodeID(n.ID):
		return invalid(where + ".id is not a node id")
	case !brief.ValidFieldName(n.Field):
		return invalid(where + ".field is not a field of the brief")
	case !validTopic(n.Topic):
		return invalid(where + ".topic is not a topic")
	}
	if err := checkText(n.Prompt, MaxPrompt, where+".prompt"); err != nil {
		return err
	}
	switch n.Type {
	case Single, Multi:
		if len(n.Options) < MinOptions || len(n.Options) > MaxOptions {
			return invalid(where + ".options does not hold 2 to 8 options")
		}
	case YesNo:
		if len(n.Options) != 2 || !n.hasOption("yes") || !n.hasOption("no") {
			return invalid(where + ".options of a yes-or-no question are not yes and no")
		}
	case Scale:
		if len(n.Options) != 0 {
			return invalid(where + ".options of a scale is not empty")
		}
	default:
		return invalid(where + ".type is not one of the types")
	}
	for j, o := range n.Options {
		ow := where + ".options[" + strconv.Itoa(j) + "]"
		if !brief.ValidChoice(o.ID) || o.ID == AnswerOther || o.ID == AnswerYouDecide || o.ID == AnswerSkip {
			return invalid(ow + ".id is not a choice id, or is a reserved word")
		}
		for _, p := range n.Options[:j] {
			if p.ID == o.ID {
				return invalid(ow + ".id repeats the id of another option")
			}
		}
		if err := checkText(o.Label, MaxLabel, ow+".label"); err != nil {
			return err
		}
		if err := checkText(o.Example, MaxExample, ow+".example"); err != nil {
			return err
		}
	}
	if n.AllowOther && n.Type != Single && n.Type != Multi {
		return invalid(where + ".allow_other is set on a question that is not a choice of options")
	}
	if n.AllowYouDecide != (n.YouDecide != "") {
		return invalid(where + ".you_decide is set exactly when allow_you_decide is")
	}
	if n.YouDecide != "" && n.YouDecide != brief.Default && n.YouDecide != brief.SupervisorInferred {
		return invalid(where + ".you_decide is not default or supervisor-inferred")
	}
	if n.AllowYouDecide && n.Default == "" {
		return invalid(where + ".allow_you_decide is set without a default")
	}
	if n.Default != "" && !n.canChoose(n.Default) {
		return invalid(where + ".default is not a choice of the question")
	}
	for j, f := range n.Invalidates {
		if !brief.ValidFieldName(f) {
			return invalid(where + ".invalidates names a field that is not one of the brief")
		}
		if f == n.Field {
			return invalid(where + ".invalidates names the node's own field")
		}
		if contains(n.Invalidates[:j], f) {
			return invalid(where + ".invalidates names a field twice")
		}
	}
	if n.When != nil {
		if _, err := n.When.shape(where+".when", 1); err != nil {
			return err
		}
	}
	return nil
}

func (n Node) hasOption(id string) bool {
	for _, o := range n.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// canChoose reports whether id is a choice of the question: an option,
// yes or no, or a step 1 to 5.
func (n Node) canChoose(id string) bool {
	if n.Type == Scale {
		return len(id) == 1 && id[0] >= '1' && id[0] <= '5'
	}
	return n.hasOption(id)
}

// canSet reports whether an answer to the node can make a condition's
// value match: a choice, or "other" when free text is allowed.
func (n Node) canSet(value string) bool {
	return n.canChoose(value) || (value == OtherChoice && n.AllowOther)
}

// checkText holds the rules of a host text: 1 to max bytes of plain text,
// no leading or trailing space, no markup characters and no web address.
func checkText(s string, max int, where string) error {
	lower := strings.ToLower(s)
	switch {
	case s == "" || len(s) > max:
		return invalid(where + " is empty or longer than " + strconv.Itoa(max) + " bytes")
	case !canon.Plain(s):
		return invalid(where + " holds a control character or bytes that are not UTF-8")
	case strings.TrimSpace(s) != s:
		return invalid(where + " starts or ends with a space")
	case strings.ContainsAny(s, "<>"):
		return invalid(where + " holds a markup character")
	case strings.Contains(lower, "://") || strings.Contains(lower, "www.") || strings.Contains(lower, "mailto:"):
		return invalid(where + " holds a web address")
	}
	return nil
}

func validTopic(s string) bool {
	if len(s) == 0 || len(s) > 16 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' {
			return false
		}
	}
	return true
}

func validVersion(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && !(i > 0 && (c == '.' || c == '-')) {
			return false
		}
	}
	return true
}

// checkAcyclic refuses a cycle in the invalidation graph.
func checkAcyclic(graph map[string][]string) error {
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int)
	var visit func(string) bool
	visit = func(f string) bool {
		switch state[f] {
		case visiting:
			return false
		case done:
			return true
		}
		state[f] = visiting
		for _, g := range graph[f] {
			if !visit(g) {
				return false
			}
		}
		state[f] = done
		return true
	}
	keys := make([]string, 0, len(graph))
	for k := range graph {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !visit(k) {
			return invalid("the invalidations form a cycle")
		}
	}
	return nil
}

// closureOf returns the fields reachable from f in the acyclic graph,
// sorted.
func closureOf(graph map[string][]string, f string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(f string) {
		for _, g := range graph[f] {
			if !seen[g] {
				seen[g] = true
				walk(g)
			}
		}
	}
	walk(f)
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func appendNew(list []string, s string) []string {
	if contains(list, s) {
		return list
	}
	return append(list, s)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
