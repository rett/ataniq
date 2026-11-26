package checkpoint

import (
	"bytes"
	"flag"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

var updateProfile = flag.Bool("update-checkpoint-profile", false, "regenerate checkpoint's specialization of assignment/wire SSHSIG")

// The upstream package exposes only the assignment event namespace. Specialize
// its internal binary primitive at source level so we can keep its public API
// and vectors intact. Derive the checked-in file from the actual Go declarations
// and detect upstream changes during the offline contract tests.
func TestProfileMatchesAssignmentWire(t *testing.T) {
	const source = "../assignment/wire/sshsig.go"
	input, err := os.ReadFile(source)
	requireOK(t, err)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, source, input, 0)
	requireOK(t, err)
	funcs := map[string]string{}
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok {
			funcs[f.Name.Name] = string(input[fset.Position(f.Pos()).Offset:fset.Position(f.End()).Offset])
		}
	}
	var out strings.Builder
	out.WriteString(`// Code generated from internal/assignment/wire/sshsig.go by TestProfileMatchesAssignmentWire. DO NOT EDIT.

package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/binary"
)
`)
	// The SSH profile constants come from the same source as the functions.
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			v := spec.(*ast.ValueSpec)
			switch v.Names[0].Name {
			case "magic", "hashName", "keyType":
				out.WriteString("const " + string(input[fset.Position(v.Pos()).Offset:fset.Position(v.End()).Offset]) + "\n")
			}
		}
	}
	for _, name := range []string{"sshString", "keyBlob", "validPrivate", "signaturePrefix", "signedData", "signBlob", "verifyBlob"} {
		source, ok := funcs[name]
		if !ok {
			t.Fatalf("upstream SSHSIG primitive %s changed", name)
		}
		switch name {
		case "signaturePrefix", "signedData", "signBlob", "verifyBlob":
			source = strings.Replace(source, "func "+name+"(", "func (p profile) "+name+"(", 1)
		}
		if name == "signBlob" || name == "verifyBlob" {
			source = strings.NewReplacer("signaturePrefix(", "p.signaturePrefix(", "signedData(", "p.signedData(").Replace(source)
		}
		source = strings.NewReplacer("[]byte(Namespace)", "[]byte(p)", "[]byte(domain)", `[]byte(string(p)+"\x00")`,
			"recordLimit", "maxFrame", "assignment.ErrLimit", "ErrRefused", "errKey", "ErrRefused", "errSignature", "ErrRefused").Replace(source)
		out.WriteString("\n" + source + "\n")
	}
	generated, err := format.Source([]byte(out.String()))
	requireOK(t, err)
	const path = "sshsig_generated.go"
	if *updateProfile {
		requireOK(t, os.WriteFile(path, generated, 0644))
	}
	current, err := os.ReadFile(path)
	requireOK(t, err)
	if !bytes.Equal(current, generated) {
		t.Fatal("SSHSIG primitive changed; review and regenerate with -update-checkpoint-profile")
	}
}
