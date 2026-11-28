package tests

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const sessionPkg = "internal/agentsession"

// Rule 8 allows the packages that decide on a session to use its vocabulary.
// Prefixes include subpackages but stop at a path component.
var decidingPackages = []string{
	"internal/leaddaemon", "internal/leadlifecycle", "internal/leadregistry",
	"cmd/ataniqd", "internal/leadcanary", "internal/leadcontrol",
}

// These packages mirror the vocabulary in their own wire types (§2.5).
var wirePackages = []string{
	"internal/clientapi", "internal/terminalconsole", "internal/leadattach",
	"internal/operatorcli", "internal/leadholder/wire", "cmd/ataniq-console",
	"internal/leadwire", "internal/consoleclient", "internal/approval/ownerwire",
	"internal/seatapp", "internal/seatlink", "internal/seatgate",
	"internal/leadchat", "internal/chat",
}

func inPackageTree(dir string, roots []string) bool {
	return slices.ContainsFunc(roots, func(root string) bool { return pathIsOrUnder(dir, root) })
}

// Walk source as well as querying the builds: rules 1–3 and 8 apply even to
// test files, or a source selected only on another platform.
func packBoundaryFiles(t *testing.T, subtree string, visit func(string)) {
	t.Helper()
	repo := os.DirFS(root(t))
	err := fs.WalkDir(repo, subtree, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name == "third_party" || skipCanaryDir(repo, name, entry.Name()) {
				return fs.SkipDir
			}
		} else if strings.HasSuffix(name, ".go") {
			visit(name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func packBoundaryParse(t *testing.T, name string, mode parser.Mode) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root(t), name), nil, mode|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func packBoundaryImports(t *testing.T, name string, check func(pkg, imp string) bool) {
	t.Helper()
	file := packBoundaryParse(t, name, parser.ImportsOnly)
	for _, spec := range file.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !check(file.Name.Name, imp) {
			t.Errorf("%s: forbidden import %q", name, imp)
		}
	}
}

// Provider-specific entries live with the packs. Key each exception to the
// exact file/import or package/declaration so a second offender is visible.
type migrationUses map[[2]string]bool

func (m migrationUses) use(source, target string) bool {
	key := [2]string{source, target}
	if _, ok := m[key]; !ok {
		return false
	}
	m[key] = true
	return true
}

func (m migrationUses) unused() []string {
	var unused []string
	for pair, used := range m {
		if !used {
			unused = append(unused, pair[0]+" "+pair[1])
		}
	}
	slices.Sort(unused)
	return unused
}

func (m migrationUses) check(t *testing.T, list string) {
	t.Helper()
	for _, entry := range m.unused() {
		t.Errorf("%s: unused migration: %s", list, entry)
	}
}

func packMigrations(t *testing.T, list string) migrationUses {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), "packs/all/testdata/boundary", list))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parsePackMigrations(list, string(b))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func parsePackMigrations(list, data string) (migrationUses, error) {
	result := migrationUses{}
	for i, line := range strings.Split(data, "\n") {
		entry, reason, _ := strings.Cut(line, "#")
		if strings.TrimSpace(entry) == "" {
			continue
		}
		f := strings.Fields(entry)
		if len(f) != 2 || !strings.HasPrefix(reason, " Migration: ") || strings.TrimSpace(strings.TrimPrefix(reason, " Migration: ")) == "" {
			return nil, fmt.Errorf("%s:%d: want two names and a migration reason", list, i+1)
		}
		key := [2]string{f[0], f[1]}
		if _, ok := result[key]; ok {
			return nil, fmt.Errorf("%s:%d: duplicate migration: %s %s", list, i+1, f[0], f[1])
		}
		result[key] = false
	}
	return result, nil
}

func holderSide(dir string) bool {
	return dir == "cmd/ataniq-holder" || (pathIsOrUnder(dir, "internal/leadholder") && !pathIsOrUnder(dir, "internal/leadholder/wire"))
}

func coreImportOK(dir, pkg, imp string) bool {
	switch imp {
	case "ataniq/packs/all":
		parts := strings.Split(dir, "/")
		return pkg == "main" && dir != "cmd/ataniq-holder" && (strings.HasPrefix(dir, "cmd/") ||
			(len(parts) >= 4 && parts[0] == "qualification" && parts[2] == "cmd"))
	case "ataniq/packs/holder": // D35 renamed the terminal composition list.
		return holderSide(dir)
	}
	return !pathIsOrUnder(imp, "ataniq/packs")
}

func TestPackBoundaryCoreImports(t *testing.T) {
	packBoundaryFiles(t, ".", func(name string) {
		if !pathIsOrUnder(name, "packs") {
			packBoundaryImports(t, name, func(pkg, imp string) bool { return coreImportOK(path.Dir(name), pkg, imp) })
		}
	})
}

// Each entry is a neutral concern a pack implements (§3 and the S0 hook and
// protocol contracts). The temporary OSC parser is covered by the stand-in
// migrations below; no pack on this baseline imports it.
var packContracts = map[string]string{
	"internal/pack":                "pack identity, declarations and registration",
	sessionPkg:                     "session claims and protocol events",
	sessionPkg + "/protocol":       "neutral codec contract",
	sessionPkg + "/hookwire":       "hook reports and extraction tables",
	"internal/inject/screen":       "screen recognition",
	"internal/inject/screen/grid":  "screen cells",
	"internal/inject/title":        "title reading",
	"internal/inject/status":       "status reports",
	"internal/inject/idle":         "idle readings",
	"internal/leadsandbox/profile": "sandbox runtime inputs",
	"internal/intake/execconfig":   "execution-config rules",
	"internal/agentlaunch":         "launch and credentials",
}

func packImportOK(name, imp string, std bool) bool {
	dir := path.Dir(name)
	isTest := strings.HasSuffix(name, "_test.go")
	target, local := strings.CutPrefix(imp, "ataniq/")
	if std || (isTest && dir == target) {
		return true
	}
	if !local {
		return false
	}
	if dir == "packs/all" {
		// The full composition suite joins packs with core, including the
		// holder list. Only its tests need that second list.
		return isTest || target == "internal/pack" || (path.Dir(target) == "packs" && target != "packs/holder")
	}
	parts := strings.Split(target, "/")
	if dir == "packs/holder" {
		// D35 and task 50: holder parts, embedded launch data and the
		// return type of ProtocolCodec. No other sibling or core package.
		return target == "internal/pack" || target == sessionPkg+"/protocol" ||
			(len(parts) == 3 && parts[0] == "packs" &&
				!slices.Contains([]string{"internal", "all", "holder"}, parts[1]) &&
				(parts[2] == "holder" || parts[2] == "launchpolicy"))
	}
	if packContracts[target] != "" || (isTest && target == "internal/pack/packtest") {
		return true
	}
	if pathIsOrUnder(target, "packs/internal") {
		return true // shared pack implementation
	}
	own := strings.Split(dir, "/")
	return len(own) >= 2 && !slices.Contains([]string{"all", "holder", "internal"}, own[1]) &&
		pathIsOrUnder(target, "packs/"+own[1])
}

func TestPackBoundaryPackImports(t *testing.T) {
	std := strings.Fields(goListOnce(t, []string{"list", "std"}))
	// Two newer codec edges (wire text validation and a holder-selection
	// test) have file-specific migrations in imports.txt.
	migrations := packMigrations(t, "imports.txt")
	packBoundaryFiles(t, "packs", func(name string) {
		packBoundaryImports(t, name, func(_, imp string) bool {
			return packImportOK(name, imp, slices.Contains(std, imp)) || migrations.use(name, imp)
		})
	})
	migrations.check(t, "imports.txt")
}

// Use the compiler's exported constants: an inferred type, or a type inherited
// in a const group, counts just as an explicit type does. Unlike the planned
// cutover, both old enum APIs still exist here; enums.txt pins their individual
// declarations until their users move to pack names.
func TestPackBoundaryNoEnum(t *testing.T) {
	allowed := packMigrations(t, "enums.txt")
	// This name rule covers uses too. Two canary tests still refer to the
	// old registry default; a new caller must resolve a pack instead.
	packBoundaryFiles(t, ".", func(name string) {
		if pathIsOrUnder(name, "packs") {
			return
		}
		b, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte("DefaultCoordinator")) {
			return
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, b, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && strings.HasPrefix(id.Name, "DefaultCoordinator") && !allowed.use(name, id.Name) {
				t.Errorf("%s: forbidden identifier %s", name, id.Name)
			}
			return true
		})
	})
	for _, tags := range []string{"", canaryTag, ownerTag, ownerTag + "," + canaryTag} {
		list := ownerGoList(t, tags, []string{"-export", "-f", "{{.ImportPath}} {{.Export}}"},
			"./internal/leadregistry", "./internal/leadsandbox/profile")
		for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
			pkgPath, archive, ok := strings.Cut(line, " ")
			if !ok || archive == "" {
				t.Fatalf("missing export data: %q", line)
			}
			imp := importer.ForCompiler(token.NewFileSet(), "gc", func(string) (io.ReadCloser, error) { return os.Open(archive) })
			pkg, err := imp.Import(pkgPath)
			if err != nil {
				t.Fatal(err)
			}
			dir := strings.TrimPrefix(pkgPath, "ataniq/")
			check := func(name string) {
				if !allowed.use(dir, name) {
					t.Errorf("tags %q: forbidden enum declaration %s.%s", tags, dir, name)
				}
			}
			for _, name := range pkg.Scope().Names() {
				obj := pkg.Scope().Lookup(name)
				typ, _ := types.Unalias(obj.Type()).(*types.Named)
				_, constant := obj.(*types.Const)
				switch dir {
				case "internal/leadregistry":
					if constant && obj.Exported() && isEnumType(typ, pkg, "Provider") {
						check(name)
					}
				case "internal/leadsandbox/profile":
					if name == "ParseRuntime" || name == "Runtimes" || (constant && isEnumType(typ, pkg, "Runtime")) {
						check(name)
					}
					if _, ok := obj.(*types.TypeName); ok && typ != nil && typ.Obj() == obj {
						for method := range typ.Methods() {
							if method.Name() == "Other" {
								check(name + ".Other")
							}
						}
					}
				}
			}
		}
	}
	allowed.check(t, "enums.txt")
}

func isEnumType(typ *types.Named, pkg *types.Package, name string) bool {
	return typ != nil && typ.Obj().Pkg() == pkg && typ.Obj().Name() == name
}

func neutralImportOK(dir, imp string) bool {
	target := strings.TrimPrefix(imp, "ataniq/")
	wire := inPackageTree(dir, wirePackages)
	if !wire && !inPackageTree(dir, decidingPackages) {
		return true
	}
	if pathIsOrUnder(target, sessionPkg) {
		return !wire && (target == sessionPkg || target == sessionPkg+"/command")
	}
	// Rule 1 separately checks the main package at a composition root.
	return !pathIsOrUnder(target, "internal/inject") && (!pathIsOrUnder(target, "packs") || target == "packs/all")
}

func driverImportOK(dir, target string, isTest bool) bool {
	switch target {
	case "packs/holder":
		// Check 4 concerns production. Rules 1 and 7 also check test
		// imports, including the codec test migration in imports.txt.
		return isTest || holderSide(dir)
	case sessionPkg + "/protocol":
		// The renamed headless contract is also the interface implemented
		// by packs and by the contract's synthetic codec.
		if pathIsOrUnder(dir, "packs") || dir == target+"/protocoltest" {
			return true
		}
	case sessionPkg + "/terminal", sessionPkg + "/headless":
	default:
		return true
	}
	return holderSide(dir) || (isTest && (dir == target || dir == "packs/all"))
}

// These edges must still require the canary tag. Compared with §2.5, three
// stand-in tests also consume the query reply. Each exception is one file.
var sessionMigrations = map[string]string{
	"internal/leadcanary/admission.go":              "ataniq/internal/inject/command", // Migration: task 31 moves the admission header rules.
	"internal/leadcanary/standin/run.go":            "ataniq/internal/inject/osc7501", // Migration: task 25 moves the report parser.
	"internal/leadcanary/standin/latereply_test.go": "ataniq/internal/inject/osc7501", // Migration: retire with the stand-in's legacy reply in task 25.
	"internal/leadcanary/standin/mutations_test.go": "ataniq/internal/inject/osc7501", // Migration: retire with the stand-in's legacy reply in task 25.
	"internal/leadcanary/standin/replay_test.go":    "ataniq/internal/inject/osc7501", // Migration: retire with the stand-in's legacy reply in task 25.
}

// Collapse the file exceptions to edges only for the tagged graph. The
// independent source pass checks each offending import against its exact file.
func sessionGraphViolations(list string, canary bool) []string {
	migrations := map[[2]string]bool{}
	if canary {
		for file, imp := range sessionMigrations {
			migrations[[2]string{path.Dir(file), imp}] = true
		}
	}
	var violations []string
	for line := range strings.Lines(list) {
		edges := strings.Fields(line)
		dir := strings.TrimPrefix(edges[0], "ataniq/")
		for _, imp := range edges[1:] {
			if !neutralImportOK(dir, imp) && !migrations[[2]string{dir, imp}] {
				violations = append(violations, fmt.Sprintf("%s imports %s", dir, imp))
			}
		}
	}
	return violations
}

func TestPackBoundarySessionNeutral(t *testing.T) {
	std := strings.Fields(goListOnce(t, []string{"list", "std"}))
	// Direct imports of production, in-package tests and external tests.
	const imports = "{{.ImportPath}} {{join .Imports \" \"}} {{join .TestImports \" \"}} {{join .XTestImports \" \"}}"
	for _, tags := range []string{"", canaryTag, ownerTag, ownerTag + "," + canaryTag} {
		list := ownerGoList(t, tags, []string{"-f", imports}, "./...")
		for _, violation := range sessionGraphViolations(list, slices.Contains(strings.Split(tags, ","), canaryTag)) {
			t.Errorf("tags %q: %s", tags, violation)
		}
	}

	migrations := migrationUses{}
	for file, imp := range sessionMigrations {
		migrations[[2]string{file, imp}] = false
	}
	packBoundaryFiles(t, ".", func(name string) {
		dir := path.Dir(name)
		isTest := strings.HasSuffix(name, "_test.go")
		packBoundaryImports(t, name, func(_, imp string) bool {
			if dir == sessionPkg {
				return slices.Contains(std, imp) || imp == "ataniq/internal/pack" || (isTest && imp == "ataniq/"+dir)
			}
			if !neutralImportOK(dir, imp) {
				return migrations.use(name, imp)
			}
			return driverImportOK(dir, strings.TrimPrefix(imp, "ataniq/"), isTest)
		})
		if (dir == sessionPkg || dir == sessionPkg+"/command") && !isTest {
			file := packBoundaryParse(t, name, 0)
			checkSessionNames(file, func(id string) { t.Errorf("%s: terminal vocabulary in %s", name, id) })
		}
	})
	migrations.check(t, "sessionMigrations")
}

// Inspect declarations and their types, including anonymous structs in an API.
// Selector expressions and names used in function bodies are not declarations.
func checkSessionNames(f *ast.File, report func(string)) {
	check := func(name string) {
		if terminalIdentifier(name) {
			report(name)
		}
	}
	exported := func(id *ast.Ident) {
		if id.IsExported() {
			check(id.Name)
		}
	}
	for _, decl := range f.Decls {
		var api ast.Node = decl
		if fn, ok := decl.(*ast.FuncDecl); ok {
			exported(fn.Name)
			api = fn.Type // skip the implementation
		}
		ast.Inspect(api, func(n ast.Node) bool {
			switch spec := n.(type) {
			case *ast.TypeSpec:
				exported(spec.Name)
			case *ast.ValueSpec:
				g, _ := decl.(*ast.GenDecl)
				typ, _ := spec.Type.(*ast.Ident)
				for _, id := range spec.Names {
					if g != nil && g.Tok == token.CONST && typ != nil && typ.Name == "Basis" &&
						slices.Contains([]string{"Screen", "BasisScreen", "ScreenBasis"}, id.Name) {
						continue // the screen basis is part of the neutral vocabulary
					}
					exported(id)
				}
			case *ast.StructType:
				for _, field := range spec.Fields.List {
					if len(field.Names) == 0 {
						check(embeddedFieldName(field.Type))
					}
					for _, id := range field.Names {
						check(id.Name)
					}
				}
			case *ast.InterfaceType:
				for _, method := range spec.Methods.List {
					for _, id := range method.Names {
						exported(id)
					}
				}
			}
			return true
		})
	}
}

func embeddedFieldName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return embeddedFieldName(e.X)
	case *ast.IndexExpr:
		return embeddedFieldName(e.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	}
	return ""
}

func terminalIdentifier(id string) bool {
	// Split an acronym before the next capitalized word (PTYReader), and
	// split ordinary camel case. Thus Enterprise does not contain Enter.
	r := []rune(id)
	start := 0
	for i := 1; i <= len(r); i++ {
		if i < len(r) && unicode.IsLetter(r[i-1]) && unicode.IsLetter(r[i]) &&
			!(unicode.IsUpper(r[i]) && (unicode.IsLower(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1])))) {
			continue
		}
		switch strings.ToLower(string(r[start:i])) {
		case "pty", "composer", "cursor", "title", "paste", "keystroke", "osc", "fence", "enter", "screen":
			return true
		}
		start = i
	}
	return false
}

func TestPackBoundaryForbiddenImports(t *testing.T) {
	const graph = "ataniq/internal/leadcanary ataniq/internal/inject/command\n" +
		"ataniq/internal/leadcanary/new ataniq/internal/inject/command\n" +
		"ataniq/internal/leadwire ataniq/internal/agentsession\n" +
		"ataniq/cmd/ataniqd ataniq/internal/agentsession ataniq/packs/all\n"
	want := []string{
		"internal/leadcanary imports ataniq/internal/inject/command",
		"internal/leadcanary/new imports ataniq/internal/inject/command",
		"internal/leadwire imports ataniq/internal/agentsession",
	}
	if got := sessionGraphViolations(graph, false); !slices.Equal(got, want) {
		t.Errorf("untagged violations = %v, want %v", got, want)
	}
	if got := sessionGraphViolations(graph, true); !slices.Equal(got, want[1:]) {
		t.Errorf("canary violations = %v, want %v", got, want[1:])
	}
	for _, tc := range []struct{ dir, imp string }{
		{"tests", "ataniq/packs/alpha"},
		{"internal/helper", "ataniq/packs/all"},
		{"cmd/ataniq-holder", "ataniq/packs/all"},
		{"cmd/ataniq-holder", "ataniq/packs/alpha/holder"},
		{"internal/leadholder/wire", "ataniq/packs/holder"},
	} {
		if coreImportOK(tc.dir, "main", tc.imp) {
			t.Errorf("%s accepted %s", tc.dir, tc.imp)
		}
	}
	for _, tc := range []struct{ file, imp string }{
		{"packs/alpha/part_test.go", "ataniq/packs/beta"},
		{"packs/alpha/part.go", "ataniq/internal/leaddaemon"},
		{"packs/holder/list.go", "ataniq/packs/alpha"},
		{"packs/holder/list_test.go", "ataniq/packs/beta/hook"},
		{"packs/alpha/part.go", "example.org/driver"},
	} {
		if packImportOK(tc.file, tc.imp, false) {
			t.Errorf("%s accepted %s", tc.file, tc.imp)
		}
	}
	for _, pkg := range slices.Concat(decidingPackages, wirePackages) {
		if neutralImportOK(pkg+"/sub", "ataniq/internal/inject/screen") {
			t.Errorf("%s accepted the transport", pkg)
		}
	}
	for _, pkg := range wirePackages {
		if neutralImportOK(pkg, "ataniq/"+sessionPkg) {
			t.Errorf("%s accepted the session model", pkg)
		}
	}
}

func TestPackBoundarySessionVocabulary(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "", `package session
const BasisScreen Basis = "screen"
type Snapshot struct { *driver.PTYReader; lastCursor int; Enterprise bool }
type Observer interface { RecordOSC() }
func NewFence() { _ = driver.Title }`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	checkSessionNames(f, func(name string) { got = append(got, name) })
	if want := []string{"PTYReader", "lastCursor", "RecordOSC", "NewFence"}; !slices.Equal(got, want) {
		t.Errorf("terminal names = %v, want %v", got, want)
	}
}

func TestPackBoundaryMigrationEntries(t *testing.T) {
	for _, line := range []string{
		"source target",
		"source target # Migration:  ",
		"source target # explanation",
		"source # Migration: retire",
		"source target extra # Migration: retire",
		"source target # Migration: retire\nsource target # Migration: retire",
	} {
		if _, err := parsePackMigrations("list", line); err == nil {
			t.Errorf("accepted malformed migration %q", line)
		}
	}
	m, err := parsePackMigrations("list", "old.go wire # Migration: retire\nstill.go wire # Migration: retire")
	if err != nil {
		t.Fatal(err)
	}
	if m.use("new.go", "wire") || !m.use("still.go", "wire") || !m.use("still.go", "wire") {
		t.Fatal("migration must match its exact pair across builds")
	}
	if got := m.unused(); !slices.Equal(got, []string{"old.go wire"}) {
		t.Errorf("unused migrations = %q", got)
	}
}
