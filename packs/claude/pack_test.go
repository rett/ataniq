package claude_test

import (
	"reflect"
	"testing"

	"ataniq/internal/pack"
	"ataniq/packs/claude"
)

func TestManifest(t *testing.T) {
	var p pack.Pack = claude.Pack{}
	want := pack.Manifest{
		Name: "claude", Class: "runtime", Vendor: "Anthropic", Display: "Claude Code", QargiName: "claude-code",
		Words: []pack.Word{
			{Text: "claude", Match: "substring"},
			{Text: "anthropic", Match: "substring"},
		},
		Builds: []pack.Build{{
			Version: "2.1.325", Transports: []pack.Transport{pack.Terminal},
			Source: "internal/inject/screen/testdata/claude-2.1.325",
		}, {
			Version: "2.1.332", Transports: []pack.Transport{pack.Terminal},
			Source: "packs/claude/launchpolicy/2.1.332.help.txt",
		}},
	}
	if got := p.Name(); got != want.Name {
		t.Errorf("Name() = %q, want %q", got, want.Name)
	}
	if got := p.Manifest(); !reflect.DeepEqual(got, want) {
		t.Errorf("Manifest() = %+v, want %+v", got, want)
	}
}
