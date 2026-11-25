package codex_test

import (
	"reflect"
	"testing"

	"ataniq/internal/pack"
	"ataniq/packs/codex"
)

func TestManifest(t *testing.T) {
	var p pack.Pack = codex.Pack{}
	want := pack.Manifest{
		Name: "codex", Class: "runtime", Vendor: "OpenAI", Display: "Codex", QargiName: "codex",
		Words: []pack.Word{
			{Text: "codex", Match: "substring"},
			{Text: "openai", Match: "substring"},
		},
		Builds: []pack.Build{{
			Version: "0.169", Aliases: []string{"0.169.0"}, Transports: []pack.Transport{pack.Terminal},
			Source: "internal/inject/screen/testdata/codex-0.169",
		}, {
			Version: "0.172.0", Transports: []pack.Transport{pack.Terminal},
			Source: "packs/codex/launchpolicy/0.172.0.help.txt",
		}},
	}
	if got := p.Name(); got != want.Name {
		t.Errorf("Name() = %q, want %q", got, want.Name)
	}
	if got := p.Manifest(); !reflect.DeepEqual(got, want) {
		t.Errorf("Manifest() = %+v, want %+v", got, want)
	}
}
