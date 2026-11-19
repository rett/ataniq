//go:build linux && !ataniq_canary

package leaddaemon

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
)

// TestNoCanaryWithoutTheTag: a build without the ataniq_canary tag cannot be
// put in canary mode, and its hello lists the two operations of version 1.
func TestNoCanaryWithoutTheTag(t *testing.T) {
	if _, has := reflect.TypeOf(Config{}).FieldByName("Canary"); has {
		t.Fatal("Config has a Canary field")
	}
	if n := reflect.TypeOf(CanaryConfig{}).NumField(); n != 0 {
		t.Fatalf("CanaryConfig has %d fields", n)
	}
	e := newEnv(t)
	r := e.startDaemon(e.config())
	ctx := t.Context()
	cl, err := clientapi.Dial(ctx, r.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	h, err := cl.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Operations, " ") != "hello status" || h.Mutations {
		t.Fatalf("hello: %v mutations %v", h.Operations, h.Mutations)
	}
	var ce *clientapi.Error
	err = cl.Call(ctx, "canary.start", map[string]string{"role": "throwaway-one/lead"}, nil)
	if !errors.As(err, &ce) || ce.Code != clientapi.CodeUnsupportedOperation {
		t.Fatalf("canary.start: %v", err)
	}
}
