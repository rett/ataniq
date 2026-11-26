package assignment

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestAdapterObligationForContradictoryIssuers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(file.Doc.Text()), " ")
	for _, obligation := range []string{
		"authenticate each issuer",
		"enroll exactly one issuer identity per route per transfer",
		"report the refused contradiction to the owner out of band",
	} {
		if !strings.Contains(doc, obligation) {
			t.Errorf("package comment omits adapter obligation %q", obligation)
		}
	}
}
