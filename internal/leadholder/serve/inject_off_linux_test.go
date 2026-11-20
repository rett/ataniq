//go:build linux && !ataniq_canary

package serve

import (
	"reflect"
	"testing"
)

// A release build's Config has nothing to give a deliverer in: the gate cannot
// be opened by a caller.
func TestReleaseBuildHasNoDeliverer(t *testing.T) {
	if n := reflect.TypeOf(InjectConfig{}).NumField(); n != 0 {
		t.Errorf("InjectConfig has %d fields in a release build", n)
	}
	if _, ok := reflect.TypeOf(Config{}).FieldByName("Deliverer"); ok {
		t.Error("Config has a Deliverer field in a release build")
	}
}
