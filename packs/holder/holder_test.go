package holder_test

import (
	"testing"

	"ataniq/internal/pack"
	"ataniq/packs/holder"
)

func TestRegister(t *testing.T) {
	r := pack.New()
	if err := holder.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := holder.Register(r); err == nil {
		t.Error("registered duplicate holder parts")
	}

	r = pack.New()
	r.Freeze()
	if err := holder.Register(r); err == nil {
		t.Error("registered holder parts in a frozen registry")
	}
}
