package sandboxnet

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Paths and constants of the resolv.conf derivation.
const (
	// HostResolvConfPath is the host file the derivation starts from.
	HostResolvConfPath = "/etc/resolv.conf"

	// SystemdUpstreamPath is the systemd-resolved file that lists the real
	// upstream servers, as opposed to the local stub.
	SystemdUpstreamPath = "/run/systemd/resolve/resolv.conf"

	// SystemdStubPath is the systemd-resolved stub file. A host
	// /etc/resolv.conf whose real path is this file is the stub.
	SystemdStubPath = "/run/systemd/resolve/stub-resolv.conf"

	// StubNameserver is the address the stub listens on.
	StubNameserver = "127.0.0.53"
)

// Bounds on untrusted resolv.conf input and on the generated output.
const (
	// MaxResolvConfBytes is the largest input accepted. Larger input is
	// refused, never truncated.
	MaxResolvConfBytes = 64 << 10

	// MaxLines is the most lines accepted in one input, comments and blank
	// lines included. More lines are refused.
	MaxLines = 512

	// MaxLineBytes is the longest line examined. A longer line is dropped as
	// malformed.
	MaxLineBytes = 1024

	// MaxNameservers is how many nameservers are kept, as the resolver does
	// (MAXNS). Loopback servers are dropped first, then the first
	// MaxNameservers that remain are kept.
	MaxNameservers = 3

	// MaxSearchDomains and MaxSearchBytes bound the search list: at most six
	// domains in at most 256 bytes. They are bounds chosen here, after the
	// historical resolver limits (MAXDNSRCH and its 256 bytes), which current
	// resolvers no longer apply.
	MaxSearchDomains = 6
	MaxSearchBytes   = 256

	// MaxOptions is how many distinct option tokens are kept.
	MaxOptions = 16

	// MaxNotes is the most notes a result carries. Per-line notes stop
	// short of it by reservedNotes, so the summary notes always fit; notes
	// that did not fit are counted in one final line.
	MaxNotes = 32

	// reservedNotes is the room kept for notes that are not about one line:
	// the "derived from" line, the indented-line and other-keyword
	// summaries, the loopback and nameserver-cap summaries, and the
	// overflow line.
	reservedNotes = 6

	// maxQuoted bounds how much of an untrusted token a note repeats.
	maxQuoted = 48
)

// Source says which input the output was derived from.
type Source string

const (
	// SourceSystemdUpstream means the systemd-resolved upstream file was used
	// because the host file is the stub.
	SourceSystemdUpstream Source = "systemd-resolved upstream file"
	// SourceHostFile means the host's own resolv.conf was used.
	SourceHostFile Source = "host resolv.conf"
)

// ErrRefused is matched, with errors.Is, by every refusal returned by Derive
// and DeriveFromFS.
var ErrRefused = errors.New("sandboxnet: resolv.conf refused")

// Refusal is the error returned when no usable resolv.conf can be derived. The
// start must not go on. Reason is a complete sentence fragment fit for an
// operator.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return "sandboxnet: resolv.conf refused: " + r.Reason }

// Is reports whether target is [ErrRefused].
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// Input is what Derive reads. All of it is untrusted.
type Input struct {
	// HostContent is the content of the host's /etc/resolv.conf.
	HostContent []byte
	// HostRealPath is where /etc/resolv.conf resolves to, or empty when
	// unknown. When it equals [SystemdStubPath] the host file is the stub.
	HostRealPath string
	// UpstreamPresent says the systemd-resolved upstream file exists.
	UpstreamPresent bool
	// UpstreamContent is its content; ignored unless UpstreamPresent.
	UpstreamContent []byte
}

// Result is the outcome of a derivation. On a refusal it is partly filled
// (Source, DroppedLoopback, Rejected, Notes) so the caller can show why.
type Result struct {
	// Content is what the holder writes for the sandbox: kept nameserver,
	// search and options lines only, preceded by one comment line.
	Content []byte
	// Source is the input the content was derived from.
	Source Source
	// Nameservers, Search and Options are the kept values.
	Nameservers []netip.Addr
	Search      []string
	Options     []string
	// DroppedLoopback counts loopback nameservers removed (127.0.0.0/8, ::1
	// and IPv4-mapped loopback).
	DroppedLoopback int
	// Rejected counts lines or addresses refused as malformed, scoped or
	// otherwise unusable.
	Rejected int
	// Notes explain what was dropped or limited, in a bounded number of
	// lines. They quote untrusted text only truncated and escaped.
	Notes []string
}

// Derive builds the resolv.conf for the sandbox.
//
// When the host file is the systemd-resolved stub (its real path is the stub
// file, or its only nameserver is 127.0.0.53) and the upstream file exists, the
// result is derived from the upstream file; otherwise from the host file.
// Loopback nameservers are dropped. A derivation that leaves no non-loopback
// nameserver is refused with a reason, because the sandbox has no route to a
// host loopback service.
func Derive(in Input) (Result, error) {
	hostStub := in.HostRealPath == SystemdStubPath
	var host parsed

	if !hostStub {
		p, err := parse(in.HostContent)
		if err != nil {
			return Result{Source: SourceHostFile}, refusal("the host resolv.conf %s", err)
		}
		host = p
		hostStub = len(p.servers) == 1 && p.servers[0] == stubAddr
	}
	if !hostStub {
		return build(SourceHostFile, host, "")
	}

	if !in.UpstreamPresent {
		return Result{Source: SourceHostFile}, refusal("the host resolv.conf is the systemd-resolved stub (%s) and %s does not exist, so no upstream nameserver is known; there is no route to a host loopback service", StubNameserver, SystemdUpstreamPath)
	}
	p, err := parse(in.UpstreamContent)
	if err != nil {
		return Result{Source: SourceSystemdUpstream}, refusal("the systemd-resolved upstream file %s", err)
	}
	return build(SourceSystemdUpstream, p, "the host resolv.conf is the systemd-resolved stub; derived from its upstream file")
}

var stubAddr = netip.MustParseAddr(StubNameserver)

// Address ranges that are never a usable nameserver. None of them reaches a
// host loopback service, but none is routed to a resolver either, so a file
// whose only servers are in them must be refused, not started with DNS that
// cannot work.
var (
	// v4compat is the deprecated IPv4-compatible range (::127.0.0.1 and so
	// on); "::" and "::1" in it are handled before and apart.
	v4compat = netip.MustParsePrefix("::/96")
	// siit is the IPv4-translated range (::ffff:0:127.0.0.1 and so on).
	siit = netip.MustParsePrefix("::ffff:0:0:0/96")
	// thisNet4 is the IPv4 "this network" block.
	thisNet4 = netip.MustParsePrefix("0.0.0.0/8")
)

func refusal(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// parsed is the bounded, validated content of one resolv.conf.
type parsed struct {
	servers  []netip.Addr // valid, distinct, in file order, loopback included
	search   []string
	options  []string
	rejected int
	notes    []string
	dropped  int // per-line notes that did not fit
	other    int // lines with a keyword that is not kept
	indented int // lines the host resolver ignores for a leading blank
}

// note keeps a per-line note while there is room beyond the reserve, and
// counts it otherwise.
func (p *parsed) note(format string, args ...any) {
	if len(p.notes) < MaxNotes-reservedNotes {
		p.notes = append(p.notes, fmt.Sprintf(format, args...))
		return
	}
	p.dropped++
}

// summary keeps a note about the file as a whole, in the reserved room.
func (p *parsed) summary(format string, args ...any) {
	p.notes = append(p.notes, fmt.Sprintf(format, args...))
}

// quote repeats an untrusted token in a note, escaped and truncated.
func quote(s string) string {
	if len(s) > maxQuoted {
		s = s[:maxQuoted] + "..."
	}
	return strconv.QuoteToASCII(s)
}

// parse reads one resolv.conf. It returns an error only for input that cannot
// be read as text within the bounds; a bad line is dropped and counted.
func parse(content []byte) (parsed, error) {
	var p parsed
	if len(content) > MaxResolvConfBytes {
		return p, fmt.Errorf("is larger than %d bytes", MaxResolvConfBytes)
	}
	if strings.IndexByte(string(content), 0) >= 0 {
		return p, errors.New("contains a NUL byte")
	}
	lines := strings.Split(string(content), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > MaxLines {
		return p, fmt.Errorf("has more than %d lines", MaxLines)
	}

	for i, raw := range lines {
		n := i + 1
		// A trailing carriage return is not stripped: the host resolver does
		// not take it for a separator, so the line is rejected as holding a
		// control byte, never accepted.
		line := raw
		if len(line) > MaxLineBytes {
			p.rejected++
			p.note("line %d dropped: longer than %d bytes", n, MaxLineBytes)
			continue
		}
		if !printable(line) {
			p.rejected++
			p.note("line %d dropped: contains a control or non-ASCII byte", n)
			continue
		}
		// As the host resolver reads a file, a keyword counts only at the
		// start of a line; a line that begins with a blank is ignored.
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if t := strings.TrimLeft(line, " \t"); t != "" && t[0] != '#' && t[0] != ';' {
				p.indented++
			}
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "nameserver":
			// As the resolver does, the address ends at the first '#' or
			// ';': what follows is a comment. What is left is validated
			// like any other address, so a loopback one is still dropped.
			rest := strings.TrimPrefix(line, "nameserver")
			if i := strings.IndexAny(rest, "#;"); i >= 0 {
				rest = rest[:i]
			}
			p.nameserver(n, strings.Fields(rest))
		case "search":
			p.searchLine(n, "search", fields[1:])
		case "domain":
			// As on the host, domain and search are alternatives and the
			// last one wins; a domain line keeps its first word and the
			// rest of the line is ignored.
			if len(fields) > 2 {
				fields = fields[:2]
			}
			p.searchLine(n, "domain", fields[1:])
		case "options":
			p.optionsLine(n, fields[1:])
		default:
			p.other++
		}
	}
	if p.indented > 0 {
		p.summary("%d line(s) starting with a blank ignored, as the host resolver does", p.indented)
	}
	if p.other > 0 {
		p.summary("%d line(s) with other keywords ignored", p.other)
	}
	return p, nil
}

// printable reports whether s holds only tab and printable ASCII.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '\t' && (c < 0x20 || c > 0x7e) {
			return false
		}
	}
	return true
}

func (p *parsed) nameserver(n int, args []string) {
	if len(args) != 1 {
		p.rejected++
		p.note("line %d dropped: a nameserver line takes exactly one address", n)
		return
	}
	tok := args[0]
	if strings.Contains(tok, "%") {
		p.rejected++
		p.note("line %d dropped: scoped address %s is not passed on", n, quote(tok))
		return
	}
	a, err := netip.ParseAddr(tok)
	if err != nil {
		p.rejected++
		p.note("line %d dropped: malformed address %s", n, quote(tok))
		return
	}
	a = a.Unmap()
	switch {
	case a.IsUnspecified():
		p.rejected++
		p.note("line %d dropped: unspecified address %s", n, quote(tok))
		return
	case a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		p.rejected++
		p.note("line %d dropped: multicast or broadcast address %s", n, quote(tok))
		return
	case a.Is6() && a.IsLinkLocalUnicast():
		p.rejected++
		p.note("line %d dropped: link-local IPv6 address %s needs a scope", n, quote(tok))
		return
	case a.Is6() && !a.IsLoopback() && (v4compat.Contains(a) || siit.Contains(a)):
		p.rejected++
		p.note("line %d dropped: address %s embeds IPv4 in a form that is not routed", n, quote(tok))
		return
	case a.Is4() && thisNet4.Contains(a):
		p.rejected++
		p.note("line %d dropped: address %s is in 0.0.0.0/8, which is not routed", n, quote(tok))
		return
	}
	for _, have := range p.servers {
		if have == a {
			return
		}
	}
	p.servers = append(p.servers, a)
}

func (p *parsed) searchLine(n int, keyword string, args []string) {
	if len(args) == 0 {
		p.rejected++
		p.search = nil
		p.note("line %d dropped: empty %s list; no search list is passed on", n, keyword)
		return
	}
	var kept []string
	size := 0
	for _, d := range args {
		if !validDomain(d) {
			p.rejected++
			p.search = nil
			p.note("line %d dropped: malformed search domain %s; no search list is passed on", n, quote(d))
			return
		}
		if len(kept) >= MaxSearchDomains || size+len(d)+1 > MaxSearchBytes {
			p.note("line %d: search list cut at %d domain(s)", n, len(kept))
			break
		}
		kept = append(kept, d)
		size += len(d) + 1
	}
	// As the resolver does, the last search or domain line wins. A malformed
	// one clears the list instead of leaving an earlier list the host no
	// longer uses.
	p.search = kept
}

// validDomain accepts a dotted name of letters, digits, hyphen and underscore,
// each label 1 to 63 bytes, the whole at most 253, with one optional final dot.
func validDomain(d string) bool {
	d = strings.TrimSuffix(d, ".")
	if d == "" || len(d) > 253 {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

func (p *parsed) optionsLine(n int, args []string) {
	if len(args) == 0 {
		p.rejected++
		p.note("line %d dropped: empty options list", n)
		return
	}
	for _, o := range args {
		if !validOption(o) {
			p.rejected++
			p.note("line %d dropped: malformed option %s", n, quote(o))
			return
		}
	}
	for _, o := range args {
		dup := false
		for _, have := range p.options {
			if have == o {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if len(p.options) >= MaxOptions {
			p.note("line %d: options beyond %d are ignored", n, MaxOptions)
			return
		}
		p.options = append(p.options, o)
	}
}

// validOption accepts name or name:digits, the name of lowercase letters,
// digits and hyphen (1 to 32 bytes), the number of 1 to 4 digits.
func validOption(o string) bool {
	name, num, hasNum := strings.Cut(o, ":")
	if name == "" || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	if !hasNum {
		return true
	}
	if num == "" || len(num) > 4 {
		return false
	}
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return false
		}
	}
	return true
}

// build turns a parsed file into the result, dropping loopback servers and
// refusing when none is left.
func build(src Source, p parsed, why string) (Result, error) {
	// The notes of the parse leave room for the notes added here: at most the
	// why line, the loopback and cap summaries and the overflow line.
	res := Result{Source: src, Rejected: p.rejected, Notes: p.notes}
	addNote := func(format string, args ...any) {
		res.Notes = append(res.Notes, fmt.Sprintf(format, args...))
	}
	if why != "" {
		res.Notes = append([]string{why}, res.Notes...)
	}

	var usable []netip.Addr
	for _, a := range p.servers {
		if a.IsLoopback() {
			res.DroppedLoopback++
			continue
		}
		usable = append(usable, a)
	}
	if res.DroppedLoopback > 0 {
		addNote("%d loopback nameserver(s) dropped", res.DroppedLoopback)
	}
	if len(usable) > MaxNameservers {
		addNote("%d nameserver(s) beyond the first %d ignored, as the resolver does", len(usable)-MaxNameservers, MaxNameservers)
		usable = usable[:MaxNameservers]
	}
	if p.dropped > 0 {
		addNote("%d further note(s) not shown", p.dropped)
	}
	if len(usable) == 0 {
		if res.DroppedLoopback == 0 {
			return res, &Refusal{Reason: fmt.Sprintf("the %s has no usable nameserver", src)}
		}
		return res, &Refusal{Reason: fmt.Sprintf("the %s has only loopback nameservers (%d dropped); there is no route from the sandbox to a host loopback service", src, res.DroppedLoopback)}
	}
	res.Nameservers = usable
	res.Search = p.search
	res.Options = p.options

	var b strings.Builder
	b.WriteString("# Generated by ataniq for the lead sandbox from the " + string(src) + ".\n")
	for _, a := range usable {
		b.WriteString("nameserver " + a.String() + "\n")
	}
	if len(p.search) > 0 {
		b.WriteString("search " + strings.Join(p.search, " ") + "\n")
	}
	if len(p.options) > 0 {
		b.WriteString("options " + strings.Join(p.options, " ") + "\n")
	}
	res.Content = []byte(b.String())
	return res, nil
}
