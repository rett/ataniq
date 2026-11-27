package enrollment

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Include the field types so an existing name cannot become a function.
// Methods are the effective method sets, including promoted methods.
var admittedSurface = map[string]string{
	"const": "Capacity ClaimUncertain Closed Corrupt EnrollmentFailed Fork HomeAhead HomeBehind IdentityMismatch Insecure Interrupted Legacy MissingHeader Occupied Retired Truncated Unknown Unreachable",
	"func":  "AppendAndAdvance DecodePrepared EncodePrepared Enroll Finalize Prepare ResumePrepared Start Submit",
	"type":  "AdvanceReceipt Binding Error Fault FileRoot Identity LedgerStore Prepared Reason Store",

	"method *Prepared": "UnmarshalJSON",
	"method *Error":    "Error Unwrap",
	"method Fault":     "After",
	"method *Store":    "Close LastReceipt Ledger",

	"field AdvanceReceipt ataniq/internal/checkpoint.ID":      "ID",
	"field AdvanceReceipt ataniq/internal/checkpoint.Receipt": "Receipt",
	"field Binding Identity":                                  "Identity",
	"field Binding ataniq/internal/assignment.Header":         "Header",
	"field Binding ataniq/internal/checkpoint.ID":             "EnrollmentID",
	"field Binding ataniq/internal/checkpoint.Receipt":        "Receipt",
	"field Binding ataniq/internal/checkpoint.Slot":           "Slot",
	"field Binding crypto/ed25519.PublicKey":                  "HomePin WitnessPin",
	"field Binding uint64":                                    "Epoch",
	"field Prepared int":                                      "Version",
	"field Prepared uint64":                                   "Epoch",
	"field Prepared crypto/ed25519.PublicKey":                 "WitnessKey",
	"field Prepared []byte":                                   "Intent",
	"field Prepared string":                                   "Proof Signature",
	"field preparedRecord Prepared":                           "Request",
	"field preparedRecord Identity":                           "Identity",
	"field completedRecord Identity":                          "Identity",
	"field completedRecord []byte":                            "Receipt",
	"field Error Reason":                                      "Reason",
	"field Error error":                                       "Err",
	"field FileRoot string":                                   "Path",
	"field Identity uint64":                                   "LedgerDevice LedgerInode RootDevice RootInode",
	"field staged []byte":                                     "Intent Proof Record",
	"field staged crypto/ed25519.PublicKey":                   "Session",
}

func surfaceChanges(pkg *types.Package) []string {
	want := make(map[string]bool)
	for group, names := range admittedSurface {
		for name := range strings.FieldsSeq(names) {
			want[group+" "+name] = true
		}
	}
	var changes []string
	seen := func(key string) {
		if !want[key] {
			changes = append(changes, "+ "+key)
		}
		delete(want, key)
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		kind := ""
		switch obj.(type) {
		case *types.Const:
			kind = "const"
		case *types.Var:
			kind = "var"
		case *types.Func:
			kind = "func"
		case *types.TypeName:
			kind = "type"
			typ := obj.Type()
			for _, recv := range []types.Type{typ, types.NewPointer(typ)} {
				methods := types.NewMethodSet(recv)
				for m := range methods.Methods() {
					if m.Obj().Exported() {
						seen("method " + types.TypeString(recv, types.RelativeTo(pkg)) + " " + m.Obj().Name())
					}
				}
			}
			if st, ok := typ.Underlying().(*types.Struct); ok {
				for f := range st.Fields() {
					// A private embedding can also promote exported fields.
					if f.Exported() || f.Embedded() {
						seen("field " + name + " " + types.TypeString(f.Type(), types.RelativeTo(pkg)) + " " + f.Name())
					}
				}
			}
		}
		if obj.Exported() {
			seen(kind + " " + name)
		}
	}
	for missing := range want {
		changes = append(changes, "- "+missing)
	}
	slices.Sort(changes)
	return changes
}

func TestAdmittedWriterSurface(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	check(t, err)
	var files []*ast.File
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
			check(t, err)
			files = append(files, f)
		}
	}
	// Use the selected toolchain's offline export data for module imports.
	cmd := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-export", "-f", "{{.ImportPath}} {{.Export}}", ".")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=")
	out, err := cmd.Output()
	check(t, err)
	exports := make(map[string]string)
	for line := range strings.Lines(string(out)) {
		path, archive, _ := strings.Cut(strings.TrimSpace(line), " ")
		exports[path] = archive
	}
	config := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})}

	const rawWriter = `(root FileRoot, record []byte) error {
		l, err := root.lock(nil)
		if err != nil { return err }
		defer l.close()
		return l.append(record)
	}`
	for _, tc := range []struct {
		name, probe string
		want        []string
	}{
		{"package", "", nil},
		{"exported variable", "var RawAppend = func" + rawWriter, []string{"+ var RawAppend"}},
		{"other receiver", "func (Binding) RawAppend" + rawWriter, []string{"+ method *Binding RawAppend", "+ method Binding RawAppend"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := files
			if tc.probe != "" {
				probe, err := parser.ParseFile(fset, "probe.go", "package enrollment\n"+tc.probe, parser.SkipObjectResolution)
				check(t, err)
				input = append(slices.Clone(files), probe)
			}
			pkg, err := config.Check("ataniq/internal/enrollment", fset, input, nil)
			check(t, err)
			if changes := surfaceChanges(pkg); !slices.Equal(changes, tc.want) {
				t.Fatalf("surface changes %q, want %q", changes, tc.want)
			}
		})
	}
}
