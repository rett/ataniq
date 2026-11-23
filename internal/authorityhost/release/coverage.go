package release

import (
	"os"
	"path"
	"sort"
	"strings"

	"ataniq/internal/authorityhost/switcher"
)

// The folders of a release whose files need a validator and a reload.
const (
	caddyDir    = "etc/caddy/"
	nftDir      = "etc/nftables/"
	systemdDir  = "etc/systemd/"
	hostRelDir  = "/opt/authority-host/releases/"
	hostCurrent = "/opt/authority-host/current/"
)

// lists are the four command lists of a release as the switch parses them,
// {release} already replaced by the folder's path on the host.
type lists struct {
	validators, reloads, health, stops [][]string
}

// checkedLists names each list with the switch's file name and the place its
// commands go.
func (l *lists) byName() []struct {
	name string
	args bool
	into *[][]string
} {
	return []struct {
		name string
		args bool
		into *[][]string
	}{
		{switcher.ValidatorsList, true, &l.validators},
		{switcher.ReloadList, false, &l.reloads},
		{switcher.HealthCmd, false, &l.health},
		{switcher.StopList, false, &l.stops},
	}
}

// unit is a systemd file of the release that starts a program: a [Service]
// section with an ExecStart.
type unit struct {
	path  string   // in the release
	name  string   // the unit's name without .service; "x@" for a template
	execs []string // the ExecStart values, non-empty
}

// coverage checks what the switch does not (see the README, "The switch":
// "that coverage is the release builder's job"): that the lists cover what
// the release ships.
//
//   - Every list parses as the switch parses it; health.cmd holds a command;
//     the others hold one too, unless the release is static, where
//     stop.list may be comments only (a static release runs no decision
//     service), and validators.list and reload.list need exactly what the
//     files below ask for.
//   - validators.list holds, for every file shipped under etc/caddy, etc/nftables
//     and etc/systemd, one validator naming it by path, once (a drop-in
//     file under a *.d folder is covered by its unit's validator and needs
//     none of its own): "caddy validate --config {release}/PATH",
//     "nft -c -f {release}/PATH", "systemd-analyze verify {release}/PATH".
//   - reload.list restarts or reloads each shipped family: caddy
//     ("systemctl restart|reload caddy"), systemd units ("systemctl
//     daemon-reload" and a "systemctl restart" of every service unit),
//     nftables ("nft -f /opt/authority-host/current/PATH" of a shipped rule
//     file, or "systemctl restart nftables").
//   - Every {release}/PROGRAM named in a list is a file of the release with
//     an execute bit.
//   - No file under etc/systemd still holds ReleasePlaceholder, which Build
//     replaces with the release's folder.
//   - A static release has no unit that starts a program; any other release
//     has at least one, which runs a binary of the release
//     (/opt/authority-host/current/bin/NAME or its own releases/<id>/bin/NAME,
//     shipped with an execute bit), is stopped in stop.list ("systemctl stop",
//     but for the units of notStopped, which decide nothing) and restarted in
//     reload.list, and no unit gates its start on
//     decisions.inhibit (the decision inhibit contract in the README: the
//     inhibit is the one guard file, the journal txn.json never being there
//     without it; a service starts while the inhibit is there and refuses
//     decisions itself while it is, by one lookup of that file, as
//     decisionguard.Check makes it; a condition on the inhibit is what is
//     looked for here, since it would keep the service from starting).
func (c *checker) coverage(root *os.Root, m switcher.Manifest, id string, files map[string]entry) {
	static := m.Components == switcher.ComponentsStatic
	relDir := hostRelDir + id
	var l lists
	ok := true
	for _, f := range l.byName() {
		if _, there := files[f.name]; !there {
			c.add(CodeListMissing, f.name)
			ok = false
			continue
		}
		data, err := readSmall(root, f.name)
		if err != nil {
			c.add(CodeListMissing, f.name)
			ok = false
			continue
		}
		if *f.into, err = switcher.ParseCommandList(f.name, data, relDir, f.args); err != nil {
			c.add(CodeListInvalid, f.name)
			ok = false
		}
	}
	units := c.units(root, files)
	if !ok {
		return
	}

	c.programs(relDir, files, &l)

	if len(l.health) == 0 {
		c.add(CodeListEmpty, switcher.HealthCmd)
	}
	if !static {
		for _, f := range l.byName() {
			if len(*f.into) == 0 && f.name != switcher.HealthCmd {
				c.add(CodeListEmpty, f.name)
			}
		}
	}
	c.validators(relDir, files, l.validators)
	c.reloadsAndStops(static, relDir, id, files, l, units)
}

// notStopped names the units that start a program and need no line in
// stop.list. stop.list stops the decision services (see the README, "Layout");
// the notifier of the seat's notices decides nothing: it is told how many
// cards wait and tells the phone, and the decision stays with the seat's
// own refusals. It is restarted by the reload list like any unit. The key is
// the unit's name, without ".service"; no other unit is excused. The value is
// the one binary of the release the unit may run: "decides nothing" is a
// property of that binary, not of a name, so a unit of this name whose
// ExecStart runs another program is not excused, and is stopped like any.
var notStopped = map[string]string{"ataniq-seat-notify": "ataniq-seat-notify"}

// excused reports whether the units of this name, the file and its drop-ins,
// need no stop line: the name is in notStopped, and every ExecStart of every
// one of them runs the release's binary that name is excused for, and no
// other. An ExecStart that runs no binary of the release at all is refused on
// its own (decision-exec), so it does not need the stop line to be refused
// again.
func excused(name string, units []unit, id string, files map[string]entry) bool {
	bin, ok := notStopped[name]
	if !ok {
		return false
	}
	for _, u := range units {
		if u.name != name {
			continue
		}
		for _, exec := range u.execs {
			if n := shippedName(exec, id, files); n != "" && n != bin {
				return false
			}
		}
	}
	return true
}

// units reads every file under etc/systemd and returns those that start a
// program. It also reports a unit that gates its start on the inhibit.
func (c *checker) units(root *os.Root, files map[string]entry) []unit {
	var out []unit
	for _, p := range paths(files) {
		if !strings.HasPrefix(p, systemdDir) {
			continue
		}
		data, err := readSmall(root, p)
		if err != nil {
			c.add(CodeUnitUnreadable, p)
			continue
		}
		if strings.Contains(string(data), ReleasePlaceholder) {
			// Not built by Build, which replaces it: systemd would run
			// the words as they are.
			c.add(CodeUnitPlacehold, p)
		}
		execs, gated := parseUnit(string(data))
		if gated {
			c.add(CodeInhibitCond, p)
		}
		if len(execs) == 0 {
			continue
		}
		out = append(out, unit{path: p, name: unitName(p), execs: execs})
	}
	return out
}

// unitName is the name of the unit a file under etc/systemd belongs to,
// without ".service": the file's own, or for a drop-in the folder's
// ("app.service.d/10-x.conf" belongs to "app").
func unitName(p string) string {
	base := path.Base(p)
	if dir := path.Base(path.Dir(p)); strings.HasSuffix(dir, ".d") {
		base = strings.TrimSuffix(dir, ".d")
	}
	return strings.TrimSuffix(base, ".service")
}

// parseUnit reads a unit file as systemd does, enough for this check: lines
// joined at a trailing backslash, comments skipped, sections, and in
// [Service] the ExecStart values (an empty one resets the list). gated is
// true for a Condition or Assert line that names the decision inhibit.
func parseUnit(text string) (execs []string, gated bool) {
	var joined []string
	var cur string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if cur != "" {
			line = cur + " " + line
			cur = ""
		}
		if strings.HasSuffix(line, `\`) {
			cur = strings.TrimSpace(strings.TrimSuffix(line, `\`))
			continue
		}
		joined = append(joined, line)
	}
	if cur != "" {
		joined = append(joined, cur)
	}
	inService := false
	for _, line := range joined {
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			inService = line == "[Service]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if (strings.HasPrefix(key, "Condition") || strings.HasPrefix(key, "Assert")) && strings.Contains(value, "decisions.inhibit") {
			gated = true
		}
		if inService && key == "ExecStart" {
			if value == "" {
				execs = nil
			} else {
				execs = append(execs, value)
			}
		}
	}
	return execs, gated
}

// programs checks every {release}/PROGRAM of the lists: a file of the
// release, with an execute bit.
func (c *checker) programs(relDir string, files map[string]entry, l *lists) {
	for _, f := range l.byName() {
		for _, argv := range *f.into {
			inside, ok := strings.CutPrefix(argv[0], relDir+"/")
			if !ok {
				continue
			}
			e, there := files[inside]
			switch {
			case !there:
				c.add(CodeProgram, inside)
			case e.mode&0o111 == 0:
				c.add(CodeProgramExec, inside)
			}
		}
	}
}

// validators checks validators.list covers each shipped file once.
func (c *checker) validators(relDir string, files map[string]entry, cmds [][]string) {
	// family dir -> path -> how many validators name it
	dirs := []string{caddyDir, nftDir, systemdDir}
	named := map[string]map[string]int{caddyDir: {}, nftDir: {}, systemdDir: {}}
	for _, argv := range cmds {
		dir, targets := validated(argv)
		if dir == "" {
			continue
		}
		for _, t := range targets {
			if p, ok := strings.CutPrefix(t, relDir+"/"); ok {
				named[dir][p]++
			}
		}
	}
	for _, p := range paths(files) {
		for _, dir := range dirs {
			if !strings.HasPrefix(p, dir) || dropIn(p) {
				continue
			}
			switch n := named[dir][p]; {
			case n == 0:
				c.add(CodeValidatorNone, p)
			case n > 1:
				c.add(CodeValidatorTwice, p)
			}
		}
	}
	var odd []string
	for _, dir := range dirs {
		for p := range named[dir] {
			if e, ok := files[p]; !ok || !e.regular() || !strings.HasPrefix(p, dir) {
				odd = append(odd, p)
			}
		}
	}
	sort.Strings(odd)
	for _, p := range odd {
		c.add(CodeValidatorOther, p)
	}
}

// dropIn reports whether p, a file under etc/systemd, lies in a *.d folder:
// the validator of its unit covers it.
func dropIn(p string) bool {
	if !strings.HasPrefix(p, systemdDir) {
		return false
	}
	for _, el := range strings.Split(path.Dir(p), "/") {
		if strings.HasSuffix(el, ".d") {
			return true
		}
	}
	return false
}

// validated returns the family folder a validator command is for and the
// files it names, or "" when the command is no validator of the three
// families: "caddy validate --config F", "nft -c -f F", "systemd-analyze
// verify F...".
func validated(argv []string) (dir string, targets []string) {
	args := argv[1:]
	switch path.Base(argv[0]) {
	case "caddy":
		if len(args) == 0 || args[0] != "validate" {
			return "", nil
		}
		for i, a := range args {
			switch {
			case (a == "--config" || a == "-c") && i+1 < len(args):
				targets = append(targets, args[i+1])
			case strings.HasPrefix(a, "--config="):
				targets = append(targets, strings.TrimPrefix(a, "--config="))
			}
		}
		return caddyDir, targets
	case "nft":
		check := false
		for i, a := range args {
			switch {
			case a == "-c" || a == "--check":
				check = true
			case (a == "-f" || a == "--file") && i+1 < len(args):
				targets = append(targets, args[i+1])
			}
		}
		if !check {
			return "", nil
		}
		return nftDir, targets
	case "systemd-analyze":
		verb := false
		for _, a := range args {
			switch {
			case strings.HasPrefix(a, "-"):
			case !verb:
				if a != "verify" {
					return "", nil
				}
				verb = true
			default:
				targets = append(targets, a)
			}
		}
		if !verb {
			return "", nil
		}
		return systemdDir, targets
	}
	return "", nil
}

// systemctl splits "systemctl [options] VERB TARGET..." into the verb and
// the targets; ok is false for any other command.
func systemctl(argv []string) (verb string, targets []string, ok bool) {
	if path.Base(argv[0]) != "systemctl" {
		return "", nil, false
	}
	for _, a := range argv[1:] {
		switch {
		case strings.HasPrefix(a, "-"):
		case verb == "":
			verb = a
		default:
			targets = append(targets, a)
		}
	}
	return verb, targets, verb != ""
}

// names reports whether the systemctl target t is the unit name (or its
// .service): for a template "x@", any instance of it.
func names(t, name string) bool {
	t = strings.TrimSuffix(t, ".service")
	if strings.HasSuffix(name, "@") {
		return strings.HasPrefix(t, name)
	}
	return t == name
}

// does reports whether a command of cmds is "systemctl VERB ... UNIT" for
// one of the verbs.
func does(cmds [][]string, name string, verbs ...string) bool {
	for _, argv := range cmds {
		verb, targets, ok := systemctl(argv)
		if !ok {
			continue
		}
		for _, v := range verbs {
			if verb != v {
				continue
			}
			for _, t := range targets {
				if names(t, name) {
					return true
				}
			}
		}
	}
	return false
}

func (c *checker) reloadsAndStops(static bool, relDir, id string, files map[string]entry, l lists, units []unit) {
	family := map[string]bool{}
	for p := range files {
		for _, dir := range []string{caddyDir, nftDir, systemdDir} {
			if strings.HasPrefix(p, dir) && files[p].regular() {
				family[dir] = true
			}
		}
	}
	if family[caddyDir] && !does(l.reloads, "caddy", "restart", "reload", "reload-or-restart") {
		c.add(CodeReloadNone, "caddy")
	}
	if family[systemdDir] {
		daemon := false
		for _, argv := range l.reloads {
			if verb, _, ok := systemctl(argv); ok && verb == "daemon-reload" {
				daemon = true
			}
		}
		if !daemon {
			c.add(CodeReloadNone, "daemon-reload")
		}
	}
	if family[nftDir] {
		ok := does(l.reloads, "nftables", "restart")
		for _, argv := range l.reloads {
			if path.Base(argv[0]) != "nft" {
				continue
			}
			for i, a := range argv {
				if (a != "-f" && a != "--file") || i+1 == len(argv) {
					continue
				}
				if p, in := strings.CutPrefix(argv[i+1], hostCurrent); in && strings.HasPrefix(p, nftDir) {
					if e, there := files[p]; there && e.regular() {
						ok = true
					}
				}
			}
		}
		if !ok {
			c.add(CodeReloadNone, "nftables")
		}
	}

	if static {
		for _, u := range units {
			c.add(CodeStaticService, u.path)
		}
		return
	}
	// The notifier alone is no service: a release that is not static runs a
	// decision service, which the notifier is not.
	service := false
	for _, u := range units {
		service = service || !excused(u.name, units, id, files)
	}
	if !service {
		c.add(CodeNoService, "")
	}
	seen := map[string]bool{}
	for _, u := range units {
		for _, exec := range u.execs {
			if !shippedBinary(exec, id, files) {
				c.add(CodeDecisionExec, u.path)
			}
		}
		if seen[u.name] {
			continue
		}
		seen[u.name] = true
		if !excused(u.name, units, id, files) && !does(l.stops, u.name, "stop") {
			c.add(CodeStopNone, u.name)
		}
		if !does(l.reloads, u.name, "restart") {
			c.add(CodeReloadNone, u.name)
		}
	}
}

// shippedBinary reports whether the ExecStart value runs a binary of this
// release: /opt/authority-host/current/bin/NAME, or its own
// /opt/authority-host/releases/ID/bin/NAME, NAME a file of bin/ with an
// execute bit. systemd's prefixes (@ - : + !) before the program are
// skipped.
func shippedBinary(exec, id string, files map[string]entry) bool {
	return shippedName(exec, id, files) != ""
}

// shippedName is the name of the binary of this release that the ExecStart
// value runs, as shippedBinary reads it, or "" when it runs none.
func shippedName(exec, id string, files map[string]entry) string {
	fields := strings.Fields(exec)
	if len(fields) == 0 {
		return ""
	}
	prog := strings.TrimLeft(fields[0], "@-:+!")
	var name string
	for _, prefix := range []string{hostCurrent + "bin/", hostRelDir + id + "/bin/"} {
		if n, ok := strings.CutPrefix(prog, prefix); ok {
			name = n
		}
	}
	if !validName(name) {
		return ""
	}
	if e, ok := files["bin/"+name]; ok && e.mode&0o111 != 0 {
		return name
	}
	return ""
}
