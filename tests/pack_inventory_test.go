package tests

// Inventory names and classify the production bindings in each Linux build.
// A row certifies a declaration, registration or import boundary. Its class
// and the effects of the handler body are reviewed when the golden changes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var updateInventory = flag.Bool("update-pack-inventory", false, "regenerate the pack inventory for review")

const inventoryFixtures = "tests/testdata/pack_inventory"
const inventoryHeader = "# Linux/amd64. Columns: binding, operation, registration or definition, handler, class, build configurations.\n" +
	"# Regenerate with: sh scripts/go-tool.sh test ./tests/ -run '^TestPackInventory$' -update-pack-inventory\n" +
	"# owner-desk is reachable by processes of the owner's user until SH-10. chat.set can turn a lead's chat to read.\n" +
	"# Classes describe reviewed effects. Review handler bodies and wiring when changing this file.\n"

type inventoryItem struct{ binding, name, registration, handler string }

func (i inventoryItem) key() string {
	return strings.Join([]string{i.binding, i.name, i.registration, i.handler}, "\t")
}

type inventoryPolicy struct {
	classes  map[string]string // reviewed separately for each binding, registration and handler
	widening map[string]bool
}

func readInventory(data string) (inventoryPolicy, error) {
	p := inventoryPolicy{map[string]string{}, map[string]bool{}}
	for _, line := range strings.Split(data, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			return p, fmt.Errorf("invalid inventory row %q", line)
		}
		key := strings.Join(f[:4], "\t")
		if _, ok := p.classes[key]; ok {
			return p, fmt.Errorf("duplicate inventory row %s", key)
		}
		p.classes[key] = f[4]
		if f[0] == "widening:" {
			p.widening[f[1]] = true
		}
	}
	return p, nil
}

const (
	holderWire  = "internal/leadholder/wire"
	hookWire    = "internal/agentsession/hookwire"
	capHandlers = "internal/leadcaps"
	ownerWire   = "internal/approval/ownerwire"
)

func contractPackage(pkg string) bool {
	return packContracts[pkg] != "" || pkg == capHandlers || within(pkg, sessionPkg)
}

func packSide(pkg string) bool {
	return contractPackage(pkg) || within(pkg, "packs") || pkg == "internal/leadholder/hookport" ||
		pkg == "cmd/ataniq-hook" || pkg == "cmd/ataniq-holder" || pkg == "internal/launchpolicy"
}

func wideningPackage(pkg string, p inventoryPolicy) bool {
	switch pkg {
	case "internal/leadregistry", "internal/leadchat", "internal/leaddaemon", "cmd/ataniqd":
		return true
	}
	return within(pkg, "internal/leadcanary") || p.widening[pkg]
}

func forbiddenPackage(pkg string, p inventoryPolicy) bool {
	switch pkg {
	case "internal/seatgate", "internal/seatlink", "internal/seatapp", "internal/seatnotice", "internal/webpush", "internal/qr", "internal/ownerlocal":
		return true
	}
	return within(pkg, "internal/approval") || wideningPackage(pkg, p)
}

// Unlike a package pattern this walk sees even a wholly unselected package.
type inventorySource struct {
	file     *ast.File
	selected bool
}
type inventoryTree struct {
	root          string
	fset          *token.FileSet
	files         map[string]*inventorySource
	rows          map[string][]string
	problems      map[string]bool
	policy        inventoryPolicy
	registrations map[string]bool // possible promoted methods in an unselected file
}

func (s *inventoryTree) refuse(format string, args ...any) {
	s.problems[fmt.Sprintf(format, args...)] = true
}

func (s *inventoryTree) walk() error {
	return fs.WalkDir(os.DirFS(s.root), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name == inventoryFixtures {
				return fs.SkipDir
			}
			if name == "." {
				return nil
			}
			mod, err := os.Lstat(filepath.Join(s.root, name, "go.mod"))
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !mod.Mode().IsRegular() {
				return nil
			}
			if !strings.Contains("/"+name, "/.") {
				switch name {
				case "third_party/patched/x-ansi", "third_party/patched/x-vt":
					s.rows[inventoryItem{"nested-module:", name, "-", "-"}.key()+"\t-"] = []string{"-"}
				default:
					s.refuse("unreviewed nested module: %s", name)
				}
			}
			return fs.SkipDir
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(filepath.Join(s.root, name))
		if err != nil {
			return err
		}
		all, bad := headerConstraints(string(src))
		for _, line := range bad {
			s.refuse("invalid constraint: %s: %s", name, line)
		}
		for _, expr := range all {
			// constraint.Eval visits both branches even when the first decides it.
			expr.Eval(func(tag string) bool {
				if !validInventoryTag(tag) {
					s.refuse("unknown build tag: %s: %s", name, tag)
				}
				return false
			})
		}
		f, err := parser.ParseFile(s.fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		s.files[name] = &inventorySource{file: f}
		return nil
	})
}

var goReleaseTag = regexp.MustCompile("^go1[.][0-9]+$")

func validInventoryTag(tag string) bool {
	// GOOS and GOARCH include the go command's reserved filename suffixes.
	const words = "aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos " +
		"386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm " +
		"unix cgo race gc gccgo ignore"
	return tag == canaryTag || tag == ownerTag || goReleaseTag.MatchString(tag) || slices.Contains(strings.Fields(words), tag)
}

// An unselected file has no types. Refuse candidate declarations conservatively,
// including a registration in a package outside any of the named bindings.
func (s *inventoryTree) untypedItem(pkg string, f *ast.File) string {
	for _, decl := range f.Decls {
		for n := range ast.Preorder(decl) {
			switch x := n.(type) {
			case *ast.InterfaceType:
				if contractPackage(pkg) && len(x.Methods.List) != 0 {
					return "interface method"
				}
			case *ast.StructType:
				for _, field := range x.Fields.List {
					if len(field.Names) == 0 {
						for n := range ast.Preorder(field.Type) {
							if id, ok := n.(*ast.Ident); ok && s.registrations[id.Name] {
								return id.Name
							}
						}
					}
				}
			}
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == "Operations" || d.Name.Name == "Handle" || pkg == holderWire && d.Recv != nil && mentions(d.Recv, "Operation") && d.Name.Name == "check" || (pkg == sessionPkg && d.Type.Results != nil && mentions(d.Type.Results, "Lowering")) {
				return d.Name.Name
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if pkg == sessionPkg && spec.Name.Name == "Lowering" {
						return spec.Name.Name
					}
				case *ast.ValueSpec:
					for i, n := range spec.Names {
						if i < len(spec.Values) {
							if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind != token.STRING {
								continue
							}
						}
						if d.Tok == token.CONST && (bindingForConst(pkg, n.Name) != "" || pkg == capHandlers || pkg == holderWire && mentions(spec, "Operation")) {
							return n.Name
						}
					}
				}
			}
		}
		if mentions(decl, "HandlerFunc") {
			return "HandlerFunc"
		}
	}
	return ""
}

func mentions(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func (s *inventoryTree) checkUnselected(configs []string) error {
	for name, src := range s.files {
		if src.selected {
			continue
		}
		if item := s.untypedItem(path.Dir(name), src.file); item != "" {
			s.refuse("unselected inventory declaration: %s: %s", name, item)
			continue
		}
		for _, tags := range configs {
			ctx := build.Default
			ctx.GOOS, ctx.GOARCH, ctx.Compiler, ctx.BuildTags = "linux", "amd64", "gc", strings.FieldsFunc(tags, func(r rune) bool { return r == ',' })
			// Match the constraints and platform suffix even for a dot/underscore file.
			ctx.OpenFile = func(string) (io.ReadCloser, error) { return os.Open(filepath.Join(s.root, name)) }
			ok, err := ctx.MatchFile(filepath.Join(s.root, path.Dir(name)), "source"+path.Base(name))
			if err != nil {
				return err
			}
			if ok {
				s.refuse("unselected production file: %s", name)
				break
			}
		}
	}
	return nil
}

type inventoryPackage struct {
	Dir, ImportPath, Export string
	GoFiles, CgoFiles, Deps []string
	Module                  *struct {
		Main            bool
		Path, GoVersion string
	}
}
type inventoryImporter struct {
	local map[string]*types.Package
	deps  types.Importer
}

func (i inventoryImporter) Import(name string) (*types.Package, error) {
	if p := i.local[name]; p != nil {
		return p, nil
	}
	return i.deps.Import(name)
}

type registrationTypes struct{ extension, handler, adapter string }

func inventoryInterfaces(module string) registrationTypes {
	return registrationTypes{module + "/internal/clientapi.Extension", module + "/internal/seatgate.Handler", module + "/internal/seatgate.HandlerFunc"}
}

type inventoryBuild struct {
	*inventoryTree
	module, config string
	interfaces     registrationTypes
	info           *types.Info
	packages       map[string]*types.Package
	files          []*ast.File
	dispatchMaps   map[types.Object]bool
	branches       map[string][]*ast.CaseClause // by operation served in a capability handler
	bodies         map[*types.Func]*ast.BlockStmt
	values         map[types.Object]ast.Expr
	items          map[inventoryItem]bool
}

func (b *inventoryBuild) rel(pkg *types.Package) string {
	return strings.TrimPrefix(pkg.Path(), b.module+"/")
}
func (b *inventoryBuild) symbol(obj types.Object) string {
	name := obj.Name()
	if obj.Pkg() == nil {
		return name
	}
	if fn, ok := obj.(*types.Func); ok && fn.Signature().Recv() != nil {
		name = types.TypeString(fn.Signature().Recv().Type(), func(*types.Package) string { return "" }) + "." + name
		name = strings.TrimPrefix(name, "*")
	}
	return b.rel(obj.Pkg()) + "." + name
}

func (s *inventoryTree) configuration(tool, tags string, regs registrationTypes) error {
	cmd := exec.Command(tool, "list", "-export", "-deps", "-json=Dir,ImportPath,Export,GoFiles,CgoFiles,Module,Deps", "-tags="+tags, "./...")
	cmd.Dir = s.root
	cmd.Env = append(canaryGoEnv(), "GOOS=linux", "GOARCH=amd64")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list [%s]: %w\n%s", tags, err, stderr.Bytes())
	}
	var pkgs []inventoryPackage
	exports := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var p inventoryPackage
		if err := dec.Decode(&p); err != nil {
			return err
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Main {
			pkgs = append(pkgs, p)
		}
	}
	config := tags
	if config == "" {
		config = "untagged"
	}
	b := inventoryBuild{inventoryTree: s, config: config, interfaces: regs,
		dispatchMaps: map[types.Object]bool{}, branches: map[string][]*ast.CaseClause{},
		info:     &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}},
		packages: map[string]*types.Package{}, bodies: map[*types.Func]*ast.BlockStmt{}, values: map[types.Object]ast.Expr{}, items: map[inventoryItem]bool{}}
	deps := importer.ForCompiler(s.fset, "gc", func(name string) (io.ReadCloser, error) { return os.Open(exports[name]) })
	for _, p := range pkgs { // go list -deps emits dependencies before their importers.
		b.module = p.Module.Path
		var files []*ast.File
		for _, base := range slices.Concat(p.GoFiles, p.CgoFiles) {
			name, err := filepath.Rel(s.root, filepath.Join(p.Dir, base))
			if err != nil {
				return err
			}
			src := s.files[filepath.ToSlash(name)]
			if src == nil {
				return fmt.Errorf("selected source outside walk: %s", name)
			}
			src.selected = true
			files = append(files, src.file)
		}
		if len(files) == 0 { // a package containing only test files
			continue
		}
		conf := types.Config{Importer: inventoryImporter{b.packages, deps}, Sizes: types.SizesFor("gc", "amd64"), GoVersion: "go" + p.Module.GoVersion}
		pkg, err := conf.Check(p.ImportPath, s.fset, files, b.info)
		if err != nil {
			return fmt.Errorf("type check [%s]: %w", config, err)
		}
		b.packages[p.ImportPath] = pkg
		b.files = append(b.files, files...)
		for _, f := range files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					b.bodies[b.info.Defs[d.Name].(*types.Func)] = d.Body
					if contractPackage(b.rel(pkg)) {
						b.anonymousMethods(pkg, b.symbol(b.info.Defs[d.Name]), d)
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if contractPackage(b.rel(pkg)) {
							switch x := spec.(type) {
							case *ast.TypeSpec:
								if _, namedInterface := x.Type.(*ast.InterfaceType); !namedInterface {
									b.anonymousMethods(pkg, b.symbol(b.info.Defs[x.Name]), x)
								}
							case *ast.ValueSpec:
								b.anonymousMethods(pkg, b.symbol(b.info.Defs[x.Names[0]]), x)
							}
						}
						if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) == len(vs.Values) {
							for i, id := range vs.Names {
								b.values[b.info.Defs[id]] = vs.Values[i]
							}
						}
					}
				}
			}
		}
		dir := b.rel(pkg)
		if wideningPackage(dir, s.policy) {
			b.add("widening:", dir, "-", "-")
		}
		if dir == "internal/launchpolicy" || within(dir, "packs") && path.Base(dir) == "launchpolicy" {
			b.add("narrowing:", dir, "-", "-")
		}
		if packSide(dir) {
			if forbiddenPackage(dir, s.policy) {
				s.refuse("pack side holds widening or owner state: %s", dir)
			}
			for _, dep := range p.Deps {
				rel, local := strings.CutPrefix(dep, b.module+"/")
				if local && forbiddenPackage(rel, s.policy) {
					s.refuse("forbidden import closure: %s -> %s", dir, rel)
				}
			}
		}
	}
	b.enumerate()
	b.registrations()
	b.record()
	return nil
}

func (b *inventoryBuild) add(binding, name, reg, handler string) {
	b.items[inventoryItem{binding, name, reg, handler}] = true
}

func bindingForConst(pkg, name string) string {
	switch {
	case pkg == hookWire:
		return "hook"
	case pkg == holderWire && strings.HasPrefix(name, "Op"):
		return "holder"
	case pkg == capHandlers && strings.HasPrefix(name, "Op"):
		return "capability"
	case pkg == "internal/leadwire" && strings.HasPrefix(name, "OpChat"):
		return "owner-desk"
	case pkg == "internal/leadwire" && strings.HasPrefix(name, "Op"):
		return "desk-lead"
	case pkg == "internal/leaddaemon" && strings.HasPrefix(name, "OpCanary"):
		return "desk-lead"
	case pkg == "cmd/ataniqd" && strings.HasPrefix(name, "trialOp"):
		return "owner-desk"
	case pkg == ownerWire && strings.HasPrefix(name, "Op"):
		return "owner-channel"
	case pkg == "internal/seatgate" && strings.HasPrefix(name, "Op"):
		return "owner-channel"
	}
	return ""
}

func (b *inventoryBuild) object(e ast.Expr) types.Object {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return b.info.ObjectOf(x)
	case *ast.SelectorExpr:
		return b.info.ObjectOf(x.Sel)
	}
	return nil
}

func (b *inventoryBuild) stringValue(e ast.Expr) (string, bool) {
	v := b.info.Types[e].Value
	if v != nil && v.Kind() == constant.String {
		return constant.StringVal(v), true
	}
	return "", false
}

func (b *inventoryBuild) lookup(qualified string) types.Object {
	last := strings.LastIndex(qualified, ".")
	if last < 0 {
		return nil
	}
	if p := b.packages[qualified[:last]]; p != nil {
		return p.Scope().Lookup(qualified[last+1:])
	}
	return nil
}

func (b *inventoryBuild) named(t types.Type, pkg, name string) bool {
	obj := b.lookup(b.module + "/" + pkg + "." + name)
	return obj != nil && types.Identical(t, obj.Type())
}

func (b *inventoryBuild) enumerate() {
	for _, pkg := range b.packages {
		dir := b.rel(pkg)
		desk := map[string]bool{}
		if dir == ownerWire {
			if f, ok := pkg.Scope().Lookup("Operations").(*types.Func); ok {
				for _, op := range b.operations(f, 0) {
					desk[op] = true
					b.add("owner-desk", op, b.symbol(f), "-")
				}
			}
			if f, ok := pkg.Scope().Lookup("GateOperations").(*types.Func); ok {
				for _, op := range b.operations(f, 0) {
					b.add("owner-channel", op, b.symbol(f), "-")
				}
			}
		}
		for _, name := range pkg.Scope().Names() {
			obj := pkg.Scope().Lookup(name)
			switch o := obj.(type) {
			case *types.Const:
				if o.Val().Kind() != constant.String {
					continue
				}
				op := constant.StringVal(o.Val())
				binding := bindingForConst(dir, name)
				switch {
				case dir == capHandlers && strings.Contains(op, "."):
					binding = "capability"
				case dir == "internal/leadwire" && strings.HasPrefix(op, "chat."):
					binding = "owner-desk"
				case dir == "internal/leadwire" && strings.HasPrefix(op, "lead."):
					binding = "desk-lead"
				}
				if dir == holderWire && b.named(o.Type(), holderWire, "Operation") {
					binding = "holder"
				}
				if binding == "" {
					continue
				}
				if desk[op] {
					binding = "owner-desk"
				}
				b.add(binding, op, b.symbol(o), "-")
				if binding == "holder" && hasWord(op, "withdraw") {
					if req := pkg.Scope().Lookup("Request"); req != nil {
						b.withdrawFields(req.Type(), b.symbol(req))
					} else {
						b.refuse("unreadable withdraw request: %s", b.symbol(o))
					}
				}
			case *types.TypeName:
				if dir == capHandlers || (dir == sessionPkg || dir == hookWire) && o.Exported() {
					b.checkFields(o.Type(), b.symbol(o))
				}
				if dir == sessionPkg && name == "Lowering" {
					st, ok := o.Type().Underlying().(*types.Struct)
					if o.IsAlias() || !ok || st.NumFields() == 0 {
						b.refuse("Lowering must have private state")
					} else {
						for f := range st.Fields() {
							if f.Exported() {
								b.refuse("exported Lowering field: %s", f.Name())
							}
						}
					}
				}
				if contractPackage(dir) {
					if it, ok := o.Type().Underlying().(*types.Interface); ok {
						b.interfaceMethods(dir, b.symbol(o), it)
					}
				}
			case *types.Func:
				if dir == sessionPkg && o.Exported() && o.Signature().Results().Len() > 0 && b.named(o.Signature().Results().At(0).Type(), sessionPkg, "Lowering") {
					b.loweringWords(o)
				}
			}
		}
	}
	for f, body := range b.bodies {
		dir := b.rel(f.Pkg())
		if body == nil {
			continue
		}
		switch {
		case dir == hookWire && (b.symbol(f) == hookWire+".Request.validate" || b.symbol(f) == hookWire+".Reply.validate"):
			kind := func(e ast.Expr) bool {
				sel, ok := e.(*ast.SelectorExpr)
				return ok && sel.Sel.Name == "Kind" && b.object(sel.X) == f.Signature().Recv()
			}
			record := func(op string) { b.add("hook", op, "decoder", b.symbol(f)) }
			for n := range ast.Preorder(body) {
				switch x := n.(type) {
				case *ast.SwitchStmt:
					if kind(x.Tag) {
						b.cases(x, record)
					}
				case *ast.BinaryExpr:
					a, c := x.X, x.Y
					if kind(c) {
						a, c = c, a
					}
					if kind(a) {
						if op, ok := b.stringValue(c); ok {
							record(op)
						} else {
							b.refuse("computed frame kind: %s", b.symbol(f))
						}
					}
				}
			}
		case (dir == hookWire || dir == sessionPkg) && strings.HasPrefix(f.Name(), "Decode"):
			// A decoder can also use imported frames and local wire structs.
			b.checkFields(f.Signature(), b.symbol(f))
			for n := range ast.Preorder(body) {
				if st, ok := n.(*ast.StructType); ok {
					b.checkFields(b.info.TypeOf(st), b.symbol(f))
				}
			}
		case dir == holderWire && b.symbol(f) == holderWire+".Operation.check":
			b.holderDecoder(f, body)
		case dir == "internal/seatgate":
			ast.Inspect(body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				sel, ok := sw.Tag.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Op" {
					return true
				}
				b.cases(sw, func(op string) { b.add("owner-channel", op, "transport", b.symbol(f)) })
				return true
			})
		}
	}
}

// A new constructor or decoder word is a reviewed change, even when its
// function keeps the same name. Refuse a value the inventory cannot read.
func (b *inventoryBuild) loweringWords(f *types.Func) {
	body := b.bodies[f]
	if body == nil {
		b.refuse("unreadable lowering: %s", b.symbol(f))
		return
	}
	found := false
	for n := range ast.Preorder(body) {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !b.named(b.info.TypeOf(lit), sessionPkg, "Lowering") || len(lit.Elts) == 0 {
			continue
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				e = kv.Value
			}
			word, ok := b.stringValue(e)
			if !ok || word == "" {
				b.refuse("unreadable lowering: %s", b.symbol(f))
				return
			}
			found = true
			b.add("lowering:", word, b.symbol(f), "-")
		}
	}
	if !found {
		b.refuse("unreadable lowering: %s", b.symbol(f))
	}
}

func (b *inventoryBuild) anonymousMethods(pkg *types.Package, enclosing string, decl ast.Node) {
	ast.Inspect(decl, func(n ast.Node) bool {
		if lit, ok := n.(*ast.InterfaceType); ok {
			b.interfaceMethods(b.rel(pkg), enclosing+".interface", b.info.TypeOf(lit).Underlying().(*types.Interface))
		}
		return true
	})
}

func (b *inventoryBuild) holderDecoder(f *types.Func, body *ast.BlockStmt) {
	// Only a constant case returning nil admits an operation. A default
	// success, arbitrary predicate or delegated validation needs review.
	bad := func() { b.refuse("unreadable holder operation decoder: %s", b.symbol(f)) }
	if len(body.List) != 2 {
		bad()
		return
	}
	sw, ok := body.List[0].(*ast.SwitchStmt)
	if !ok || b.object(sw.Tag) != f.Signature().Recv() {
		bad()
		return
	}
	last, ok := body.List[1].(*ast.ReturnStmt)
	if !ok || len(last.Results) != 1 {
		bad()
		return
	}
	call, ok := last.Results[0].(*ast.CallExpr)
	if !ok || b.object(call.Fun) == nil || b.symbol(b.object(call.Fun)) != holderWire+".invalidf" {
		bad()
		return
	}
	for _, stmt := range sw.Body.List {
		c := stmt.(*ast.CaseClause)
		if len(c.List) == 0 || len(c.Body) != 1 {
			bad()
			return
		}
		r, ok := c.Body[0].(*ast.ReturnStmt)
		if !ok || len(r.Results) != 1 || b.object(r.Results[0]) != types.Universe.Lookup("nil") {
			bad()
			return
		}
	}
	b.cases(sw, func(op string) { b.add("holder", op, "decoder", b.symbol(f)) })
}

func (b *inventoryBuild) interfaceMethods(dir, reg string, it *types.Interface) {
	binding := "contract"
	if dir == capHandlers {
		binding = "capability"
	}
	for m := range it.Methods() {
		item := inventoryItem{binding, m.Name(), reg, b.symbol(m)}
		b.items[item] = true
		if b.policy.classes[item.key()] == "configure" || hasWord(m.Name(), "configure") {
			args := m.Signature().Params()
			if args.Len() != 3 || types.TypeString(args.At(0).Type(), nil) != "context.Context" || !b.named(args.At(1).Type(), sessionPkg, "SessionRef") || !b.named(args.At(2).Type(), sessionPkg, "Lowering") {
				b.refuse("configure requires context, SessionRef and Lowering: %s.%s", reg, m.Name())
			}
		}
	}
}

// JSON names can join words without delimiters (apikey, authtoken). These
// stems cover those compounds as well as inflected field and operation names.
func hasDecidingWord(name string) bool {
	const stems = "decision decid verdict approv assertion challeng grant token ticket signatur credential pair key"
	lower := strings.ToLower(name)
	for stem := range strings.FieldsSeq(stems) {
		if strings.Contains(lower, stem) {
			return true
		}
	}
	return false
}

const wideningStems = "allow enabl rais polic set on switch mode permit unlock trust widen bypass escalat elevat"

var inventoryWords = regexp.MustCompile("[A-Z]+[a-z]*|[a-z]+")
var initialismEnd = regexp.MustCompile("([A-Z])([A-Z][a-z])")

func nameParts(name string) []string {
	return inventoryWords.FindAllString(initialismEnd.ReplaceAllString(name, "$1 $2"), -1)
}

func hasWord(name, words string) bool {
	for _, part := range nameParts(name) {
		if slices.Contains(strings.Fields(words), strings.ToLower(part)) {
			return true
		}
	}
	return false
}

func hasWordStem(name, stems string) bool {
	parts := nameParts(name)
	for _, p := range parts {
		for stem := range strings.FieldsSeq(stems) {
			if strings.HasPrefix(strings.ToLower(p), stem) {
				return true
			}
		}
	}
	return false
}

// Follow the wire type, including nested fields and imported parameter types.
func inventoryFields(t types.Type, seen map[types.Type]bool, visit func(*types.Var, string)) {
	if seen[t] {
		return
	}
	seen[t] = true
	switch x := t.Underlying().(type) {
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			f := x.Field(i)
			tag := strings.Split(reflect.StructTag(x.Tag(i)).Get("json"), ",")[0]
			visit(f, tag)
			if f.Exported() || f.Embedded() {
				inventoryFields(f.Type(), seen, visit)
			}
		}
	case *types.Pointer:
		inventoryFields(x.Elem(), seen, visit)
	case *types.Array:
		inventoryFields(x.Elem(), seen, visit)
	case *types.Slice:
		inventoryFields(x.Elem(), seen, visit)
	case *types.Map:
		inventoryFields(x.Elem(), seen, visit)
	case *types.Interface:
		for m := range x.Methods() {
			inventoryFields(m.Type(), seen, visit)
		}
	case *types.Signature:
		for _, tuple := range []*types.Tuple{x.Params(), x.Results()} {
			for v := range tuple.Variables() {
				inventoryFields(v.Type(), seen, visit)
			}
		}
	}
}

func (b *inventoryBuild) checkFields(t types.Type, name string) {
	inventoryFields(t, map[types.Type]bool{}, func(f *types.Var, tag string) {
		if f.Exported() && hasDecidingWord(f.Name()) || hasDecidingWord(tag) {
			b.refuse("forbidden wire field: %s.%s", name, f.Name())
		}
	})
}
func (b *inventoryBuild) withdrawFields(t types.Type, name string) {
	inventoryFields(t, map[types.Type]bool{}, func(f *types.Var, tag string) {
		if hasWord(f.Name(), "text body payload") || hasWord(tag, "text body payload") || strings.EqualFold(f.Name(), "message") || tag == "message" {
			b.refuse("withdraw carries a message: %s.%s", name, f.Name())
		}
	})
}

func (b *inventoryBuild) operations(f *types.Func, depth int) []string {
	bad := func() []string { b.refuse("computed Operations: %s", b.symbol(f)); return nil }
	body := b.bodies[f]
	if body == nil || len(body.List) != 1 || f.Signature().Params().Len() != 0 {
		return bad()
	}
	r, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(r.Results) != 1 {
		return bad()
	}
	switch x := ast.Unparen(r.Results[0]).(type) {
	case *ast.CompositeLit:
		if len(x.Elts) == 0 {
			return bad()
		}
		var ops []string
		for _, e := range x.Elts {
			op, ok := b.stringValue(e)
			if !ok || slices.Contains(ops, op) {
				return bad()
			}
			ops = append(ops, op)
		}
		return ops
	case *ast.CallExpr:
		next, ok := b.object(x.Fun).(*types.Func)
		if !ok || len(x.Args) != 0 || next.Signature().Recv() != nil {
			return bad()
		}
		if depth == 3 {
			b.refuse("Operations exceeds three calls: %s", b.symbol(next))
			return nil
		}
		return b.operations(next, depth+1)
	}
	return bad()
}

func (b *inventoryBuild) cases(sw *ast.SwitchStmt, visit func(string)) {
	for _, stmt := range sw.Body.List {
		for _, e := range stmt.(*ast.CaseClause).List {
			op, ok := b.stringValue(e)
			if !ok {
				b.refuse("nonconstant operation case: %s", b.fset.Position(e.Pos()))
				continue
			}
			visit(op)
		}
	}
}

type inventoryDispatch struct {
	served map[string]string
	caps   map[string]string
	seen   map[*types.Var]bool
}

// Follow the Handle operation argument into module functions, using object
// identity for both the argument and the callee. Other switches in the
// same package (client code, for example) do not register operations.
func (b *inventoryBuild) dispatch(f *types.Func, arg *types.Var, d *inventoryDispatch) {
	if d.seen[arg] || b.bodies[f] == nil {
		return
	}
	d.seen[arg] = true
	serve := func(op string) {
		if prev := d.served[op]; prev != "" && prev != b.symbol(f) {
			b.refuse("ambiguous dispatch: %s: %s", b.symbol(f), op)
		}
		d.served[op] = b.symbol(f)
	}
	ast.Inspect(b.bodies[f], func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SwitchStmt:
			if b.object(x.Tag) == arg {
				b.cases(x, serve)
				if b.rel(f.Pkg()) == capHandlers {
					for _, stmt := range x.Body.List {
						c := stmt.(*ast.CaseClause)
						for _, e := range c.List {
							if op, ok := b.stringValue(e); ok {
								b.branches[op] = append(b.branches[op], c)
							}
						}
					}
				}
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				a, c := x.X, x.Y
				if b.object(c) == arg {
					a, c = c, a
				}
				if b.object(a) == arg {
					if op, ok := b.stringValue(c); ok && op != "" {
						serve(op)
					}
				}
			}
		case *ast.IndexExpr:
			if b.object(x.Index) != arg {
				break
			}
			obj := b.object(x.X)
			if obj == nil {
				break
			}
			lit, ok := b.values[obj].(*ast.CompositeLit)
			if !ok {
				b.refuse("unreadable operation map: %s", b.symbol(f))
				break
			}
			b.dispatchMaps[obj] = true
			for _, elem := range lit.Elts {
				kv, ok := elem.(*ast.KeyValueExpr)
				if !ok {
					b.refuse("unreadable operation map: %s", b.symbol(f))
					break
				}
				op, ok := b.stringValue(kv.Key)
				if !ok {
					b.refuse("computed operation map key: %s", b.symbol(f))
					break
				}
				if value, ok := b.stringValue(kv.Value); ok {
					d.caps[op] = b.symbol(obj) + "=" + value
				} else if fn, ok := b.object(kv.Value).(*types.Func); ok {
					d.served[op] = b.symbol(fn)
				} else {
					b.refuse("unreadable operation map value: %s: %s", b.symbol(f), op)
				}
			}
		case *ast.CallExpr:
			next, ok := b.object(x.Fun).(*types.Func)
			if !ok || b.bodies[next] == nil {
				break
			}
			// After dispatch the gate also passes op to refusal accounting
			// and logging. Their cases and map lookups do not serve a request.
			if b.symbol(next) == "internal/approval/gatehost.countedFor" || b.symbol(next) == "internal/approval/gatehost.Handler.refused" {
				break
			}
			args := next.Signature().Params()
			for i, e := range x.Args {
				if b.object(e) == arg && i < args.Len() && types.Identical(args.At(i).Type(), arg.Type()) {
					b.dispatch(next, args.At(i), d)
				}
			}
		}
		return true
	})
}

func (b *inventoryBuild) register(reg string, handle *types.Func, list *types.Func) {
	if handle == nil {
		b.refuse("unreadable handler: %s", reg)
		return
	}
	d := inventoryDispatch{map[string]string{}, map[string]string{}, map[*types.Var]bool{}}
	var opArg *types.Var
	for arg := range handle.Signature().Params().Variables() {
		if types.Identical(arg.Type(), types.Typ[types.String]) {
			if opArg != nil {
				b.refuse("ambiguous operation argument: %s", reg)
				return
			}
			opArg = arg
		}
	}
	if opArg == nil {
		b.refuse("missing operation argument: %s", reg)
		return
	}
	b.dispatch(handle, opArg, &d)
	if list == nil {
		for op, cap := range d.caps {
			b.add("owner-channel", op, reg, "map:"+cap)
		}
		for op, handler := range d.served {
			b.add("owner-channel", op, reg, handler)
		}
		if len(d.served) == 0 && len(d.caps) == 0 {
			b.refuse("empty seat handler: %s", reg)
		}
		for op := range d.served {
			if _, ok := d.caps[op]; !ok {
				b.refuse("seat dispatch without capability: %s: %s", reg, op)
			}
		}
		for op := range d.caps {
			if _, ok := d.served[op]; !ok {
				b.refuse("seat capability without dispatch: %s: %s", reg, op)
			}
		}
		return
	}
	ops := b.operations(list, 0)
	if ops == nil {
		return
	}
	binding := "desk-lead"
	switch reg {
	case "internal/leaddaemon.ownerExt", "internal/leaddaemon.chatExt", "cmd/ataniqd.trialExt":
		binding = "owner-desk"
	default:
		if strings.HasPrefix(reg, capHandlers+".") {
			binding = "capability"
		}
	}
	for _, op := range ops {
		handler := d.served[op]
		if handler == "" {
			b.refuse("registration without dispatch: %s: %s", reg, op)
			continue
		}
		if binding == "owner-desk" && op == "seat.pair" {
			// TestOwnerSeatPairIsRefusedToEveryConnection pins the effect.
			// The inventory pins its registration and dispatch independently.
			handler += " [registered, refused by source]"
		}
		b.add(binding, op, reg, handler)
	}
	for op := range d.served {
		if !slices.Contains(ops, op) {
			b.refuse("dispatch without registration: %s: %s", reg, op)
		}
	}
}

func (b *inventoryBuild) registrations() {
	for _, name := range []string{b.interfaces.extension, b.interfaces.handler} {
		target := b.lookup(name)
		if target == nil {
			continue
		}
		it := target.Type().Underlying().(*types.Interface)
		for _, obj := range b.info.Defs {
			o, ok := obj.(*types.TypeName)
			if !ok || o.IsAlias() || types.IsInterface(o.Type()) {
				continue
			}
			ptr := types.NewPointer(o.Type())
			method := func(n string) *types.Func {
				m, _, _ := types.LookupFieldOrMethod(ptr, false, o.Pkg(), n)
				fn, _ := m.(*types.Func)
				return fn
			}
			// Implements is unspecified for an uninstantiated generic type.
			// Refuse possible handlers, including promoted pointer methods.
			if named, ok := o.Type().(*types.Named); ok && named.TypeParams().Len() > 0 {
				if method("Operations") != nil || method("Handle") != nil {
					b.refuse("generic registration requires review: %s", b.symbol(o))
				}
				continue
			}
			if !types.Implements(ptr, it) {
				continue
			}
			b.inventoryTree.registrations[o.Name()] = true
			reg := b.symbol(o)
			if o.Pkg().Path()+"."+o.Name() == b.interfaces.adapter {
				continue
			}
			var list *types.Func
			if name == b.interfaces.extension {
				list = method("Operations")
			}
			b.register(reg, method("Handle"), list)
		}
	}
	adapter := b.lookup(b.interfaces.adapter)
	for _, file := range b.files {
		ast.PreorderStack(file, nil, func(n ast.Node, parents []ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok && adapter != nil {
				for i, lhs := range assign.Lhs {
					if t := b.info.TypeOf(lhs); t != nil && types.Identical(t, adapter.Type()) {
						if len(assign.Rhs) != len(assign.Lhs) {
							b.refuse("unreadable HandlerFunc registration: %s", b.fset.Position(lhs.Pos()))
						} else if value := assign.Rhs[i]; !types.Identical(b.info.TypeOf(value), adapter.Type()) {
							b.adapterFunction(adapter, value)
						}
					}
				}
			}
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			obj := b.info.Uses[id]
			if obj == nil {
				return true
			}
			expr := ast.Expr(id)
			if sel, ok := parents[len(parents)-1].(*ast.SelectorExpr); ok && sel.Sel == id {
				expr, parents = sel, parents[:len(parents)-1]
			}
			parent := parents[len(parents)-1]
			if b.dispatchMaps[obj] {
				// A literal's keys describe the live map only while all uses
				// are reads. Refuse aliases, mutation and computed replacement.
				index, ok := parent.(*ast.IndexExpr)
				if !ok || index.X != expr || b.info.Types[index].Assignable() && inventoryLHS(index, parents[:len(parents)-1]) {
					b.refuse("mutable operation map: %s", b.symbol(obj))
				}
			}
			if obj == adapter {
				switch x := parent.(type) {
				case *ast.CallExpr:
					if x.Fun == expr && len(x.Args) == 1 {
						b.adapterFunction(adapter, x.Args[0])
						return true
					}
				case *ast.ValueSpec:
					if x.Type == expr && len(x.Values) == len(x.Names) {
						for _, v := range x.Values {
							b.adapterFunction(adapter, v)
						}
						return true
					}
				case *ast.Field:
					// The forwarding method declares a receiver of this type.
					if f, ok := parents[len(parents)-3].(*ast.FuncDecl); ok && f.Recv == parents[len(parents)-2] {
						if f.Name.Name == "Handle" {
							b.checkForwarder(b.info.Defs[f.Name].(*types.Func))
						}
						return true
					}
				}
				b.refuse("unreadable HandlerFunc registration: %s", b.fset.Position(id.Pos()))
			}
			return true
		})
	}
}

// An adapter is transparent only if Handle calls its receiver with the
// original arguments in order. A dispatching method is a handler in its own right.
func (b *inventoryBuild) checkForwarder(f *types.Func) {
	bad := func() { b.refuse("non-forwarding HandlerFunc: %s", b.symbol(f)) }
	body := b.bodies[f]
	if body == nil || len(body.List) != 1 {
		bad()
		return
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		bad()
		return
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	params := f.Signature().Params()
	if !ok || b.object(call.Fun) != f.Signature().Recv() || len(call.Args) != params.Len() {
		bad()
		return
	}
	for i, arg := range call.Args {
		if b.object(arg) != params.At(i) {
			bad()
			return
		}
	}
}

func inventoryLHS(index ast.Expr, parents []ast.Node) bool {
	switch stmt := parents[len(parents)-1].(type) {
	case *ast.AssignStmt:
		return slices.Contains(stmt.Lhs, index)
	case *ast.IncDecStmt:
		return true
	}
	return false
}

func (b *inventoryBuild) adapterFunction(adapter types.Object, value ast.Expr) {
	fn, ok := b.object(value).(*types.Func)
	if !ok || b.bodies[fn] == nil {
		b.refuse("unreadable HandlerFunc registration: %s", b.fset.Position(value.Pos()))
		return
	}
	b.register(b.symbol(adapter)+"("+b.symbol(fn)+")", fn, nil)
}

func ownerBinding(binding string) bool { return binding == "owner-channel" || binding == "owner-desk" }

func (b *inventoryBuild) record() {
	for item := range b.items {
		class := b.policy.classes[item.key()]
		switch {
		case strings.HasSuffix(item.binding, ":"):
			if item.binding == "lowering:" {
				class = "configure"
			} else {
				class = "-"
			}
		case ownerBinding(item.binding):
			want := "owner"
			if item.binding == "owner-desk" {
				want = "owner-desk"
			}
			if class != "" && class != want {
				b.refuse("invalid class: %s %s: %s", item.binding, item.name, class)
			}
			class = want
			if want == "owner-desk" && item.name != "chat.set" && widensDesk(item.name) {
				b.refuse("desk widens lead reach: %s", item.name)
			}
		default:
			ticket := item.binding == "desk-lead" && (item.name == "lead.ticket" || item.name == "canary.ticket") || item.binding == "holder" && item.name == "ticket"
			if ticket {
				if class != "" && class != "attach" {
					b.refuse("ticket class must be attach: %s", item.name)
				}
				class = "attach"
			} else if hasDecidingWord(item.name) || hasWordStem(item.name, wideningStems) {
				b.refuse("forbidden operation name: %s %s", item.binding, item.name)
			}
			if class == "" {
				class = "REVIEW"
			} else if !slices.Contains(strings.Fields("read observe report lifecycle deliver attach configure"), class) ||
				(item.binding == "capability" && item.handler != "-" && class != "read" && class != "configure") {
				b.refuse("invalid class: %s %s: %s", item.binding, item.name, class)
			}
			if class == "configure" && item.binding != "contract" && !(item.binding == "capability" && !strings.Contains(item.name, ".")) {
				b.checkConfigureWire(item)
			}
		}
		key := item.key() + "\t" + class
		b.rows[key] = append(b.rows[key], b.config)
	}
}

func widensDesk(op string) bool {
	if hasWordStem(op, "lowering") && hasWordStem(op, "clear remove reset") {
		return true
	}
	return hasWordStem(op, "hook remote chat permission transport delivery send policy") &&
		hasWordStem(op, "on enabl allow switch set rais unlock widen bypass escalat elevat")
}

// Read the parameter type from the decoder called in this operation's
// dispatch case. An unrelated correctly shaped type cannot qualify a handler.
func (b *inventoryBuild) checkConfigureWire(item inventoryItem) {
	valid := false
	for _, branch := range b.branches[item.name] {
		for n := range ast.Preorder(branch) {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				continue
			}
			fn, ok := b.object(call.Fun).(*types.Func)
			if !ok {
				continue
			}
			switch b.symbol(fn) {
			case "encoding/json.Unmarshal", "encoding/json/v2.Unmarshal", capHandlers + ".decodeStrict":
			default:
				continue
			}
			dst, ok := b.info.TypeOf(call.Args[1]).Underlying().(*types.Pointer)
			if !ok || !b.loweringPayload(dst.Elem()) {
				b.refuse("configure wire requires command envelope and Lowering: %s", item.name)
				return
			}
			valid = true
		}
	}
	if !valid {
		b.refuse("configure wire requires command envelope and Lowering: %s", item.name)
	}
}

func (b *inventoryBuild) loweringPayload(t types.Type) bool {
	st, ok := t.Underlying().(*types.Struct)
	if !ok || st.NumFields() != 2 {
		return false
	}
	var envelope, lowering bool
	for f := range st.Fields() {
		envelope = envelope || b.named(f.Type(), sessionPkg, "CommandEnvelope")
		lowering = lowering || f.Exported() && b.named(f.Type(), sessionPkg, "Lowering")
	}
	return envelope && lowering
}

// checkInventory has no test assertions or fixture-specific rules. Errors
// from tools, parsing or typing are distinct from policy diagnostics.
func checkInventory(root, tool string, configs []string, regs registrationTypes, expected string) (string, []string, error) {
	p, err := readInventory(expected)
	if err != nil {
		return "", nil, err
	}
	s := inventoryTree{
		root: root, fset: token.NewFileSet(), files: map[string]*inventorySource{},
		rows: map[string][]string{}, problems: map[string]bool{}, policy: p,
		registrations: map[string]bool{},
	}
	for _, name := range []string{regs.extension, regs.handler, regs.adapter} {
		s.registrations[name[strings.LastIndex(name, ".")+1:]] = true
	}
	if err := s.walk(); err != nil {
		return "", nil, err
	}
	if len(s.problems) == 0 {
		for _, tags := range configs {
			if err := s.configuration(tool, tags, regs); err != nil {
				return "", nil, err
			}
		}
		if err := s.checkUnselected(configs); err != nil {
			return "", nil, err
		}
	}
	// Shared owner names are allowed on both owner bindings. Check the
	// union so disjoint build constraints cannot hide a crossed binding.
	public := map[string]bool{}
	privileged := map[string]bool{}
	publicHandlers := map[string]bool{}
	ownerHandlers := map[string]bool{}
	for key := range s.rows {
		f := strings.Split(key, "\t")
		if ownerBinding(f[0]) {
			privileged[f[1]] = true
			ownerHandlers[f[3]] = true
		} else if !strings.HasSuffix(f[0], ":") {
			public[f[1]] = true
			publicHandlers[f[3]] = true
		}
	}
	for op := range public {
		if privileged[op] {
			s.refuse("owner and non-owner bindings share operation: %s", op)
		}
	}
	for handler := range publicHandlers {
		if handler != "-" && ownerHandlers[handler] {
			s.refuse("owner and non-owner bindings share handler: %s", handler)
		}
	}
	var out strings.Builder
	out.WriteString(inventoryHeader)
	for _, row := range slices.Sorted(maps.Keys(s.rows)) {
		fmt.Fprintf(&out, "%s\t%s\n", row, strings.Join(s.rows[row], " "))
	}
	return out.String(), slices.Sorted(maps.Keys(s.problems)), nil
}

func inventoryDiff(expected, actual string) []string {
	lines := func(data string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(data, "\n") {
			if l != "" && l[0] != '#' {
				m[l] = true
			}
		}
		return m
	}
	want, got := lines(expected), lines(actual)
	var diff []string
	for l := range want {
		if !got[l] {
			diff = append(diff, "stale inventory row: "+l)
		}
	}
	for l := range got {
		if !want[l] {
			diff = append(diff, "missing inventory row: "+l)
		}
	}
	slices.Sort(diff)
	return diff
}

func TestPackInventory(t *testing.T) {
	dir := root(t)
	file := filepath.Join(dir, "tests/testdata/pack_inventory.golden")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got, problems, err := checkInventory(dir, canaryGoTool(t), closureTagSets, inventoryInterfaces("ataniq"), string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	if *updateInventory {
		if err := os.WriteFile(file, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "\tREVIEW\t") {
			t.Fatal("assign each REVIEW row its reviewed class in the golden")
		}
	} else if string(data) != got {
		t.Fatalf("inventory changed; review the diff, regenerate with -update-pack-inventory and classify new rows:\n%s", strings.Join(inventoryDiff(string(data), got), "\n"))
	}
}

func TestPackInventoryDecidingWords(t *testing.T) {
	for word := range strings.FieldsSeq("Approved Approves Approval Grant Grants Granted Token Tokens Key Keys Decisions Paired apikey api_key approvalToken authtoken") {
		if !hasDecidingWord(word) {
			t.Errorf("accepted deciding word %q", word)
		}
	}
	if !widensDesk("owner.hooks-enabled") {
		t.Error("accepted widening desk operation")
	}
}

func TestPackInventoryRefusals(t *testing.T) {
	folder := filepath.Join(root(t), inventoryFixtures)
	cases, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	// Each design case is a separate required input. Removing a tree must
	// not silently turn its check green.
	for _, required := range strings.Fields("seat-capability seat-dispatch computed-operations bool-configure capability-mode " +
		"widening-import both-only desk-hook-on withdraw-body four-calls handler-func unknown-tag other-os nested-module excluded-capability hook-field session-tag plural-operation generic-extension multi-build grid-method") {
		if !slices.ContainsFunc(cases, func(e fs.DirEntry) bool { return e.IsDir() && e.Name() == required }) {
			t.Errorf("missing refusal fixture %s", required)
		}
	}
	tool := canaryGoTool(t)
	// Each fixture runs its tag sets in golden order. Limit the number of
	// concurrent go list processes even on hosts with a high -parallel.
	slots := make(chan struct{}, min(4, runtime.GOMAXPROCS(0)))
	for _, entry := range cases {
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			src := filepath.Join(folder, entry.Name())
			// Materialize tagged .go.in files outside testdata. The tag
			// placement guards intentionally forbid tagged Go files there.
			dir := t.TempDir()
			tree := os.DirFS(src)
			err := fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				dest := filepath.Join(dir, name)
				if strings.HasSuffix(name, ".go.in") {
					dest = strings.TrimSuffix(dest, ".in")
				}
				if d.IsDir() {
					return os.MkdirAll(dest, 0755)
				}
				data, err := fs.ReadFile(tree, name)
				if err != nil {
					return err
				}
				return os.WriteFile(dest, data, 0644)
			})
			if err != nil {
				t.Fatal(err)
			}
			want, err := fs.ReadFile(tree, "want")
			if err != nil {
				t.Fatal(err)
			}
			golden, err := fs.ReadFile(tree, "inventory.golden")
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			got, problems, err := checkInventory(dir, tool, closureTagSets, inventoryInterfaces("inventory.test"), string(golden))
			if err != nil {
				t.Fatal(err)
			}
			if len(problems) == 0 {
				problems = inventoryDiff(string(golden), got)
			}
			if !slices.Equal(problems, []string{strings.TrimSuffix(string(want), "\n")}) {
				t.Fatalf("want %q; got %q", strings.TrimSpace(string(want)), problems)
			}
		})
	}
}
