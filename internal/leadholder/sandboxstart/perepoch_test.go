//go:build linux && ataniq_canary

package sandboxstart

import (
	"strings"
	"testing"
)

// TestReadInputsRefusesThePerEpochKeys shows the two statements about a start
// are not keys of the file: a file that names either is refused, with either
// spelling of the name and with a value of true or false.
func TestReadInputsRefusesThePerEpochKeys(t *testing.T) {
	_, data := sampleInputsJSON(t)
	text := string(data)
	for _, key := range []string{"no_lead_has_run", "previous_instance_stopped", "NoLeadHasRun", "PreviousInstanceStopped"} {
		for _, value := range []string{"true", "false"} {
			t.Run(key+"="+value, func(t *testing.T) {
				mutated := strings.Replace(text, `"canary": true,`, `"canary": true, "`+key+`": `+value+`,`, 1)
				if mutated == text {
					t.Fatal("the sample has no canary key")
				}
				_, err := ReadInputs(writeInputs(t, []byte(mutated), 0o600))
				if err == nil {
					t.Fatal("accepted")
				}
				wantInputsError(t, err, "unknown key")
				if !strings.Contains(err.Error(), key) {
					t.Errorf("the refusal does not name the key: %v", err)
				}
			})
		}
	}
}

// TestPerEpochFieldsAreNeverRead shows a file cannot carry the two statements
// even when the sample has them true: they reach Run only through the struct.
func TestPerEpochFieldsAreNeverRead(t *testing.T) {
	in, data := sampleInputsJSON(t)
	if !in.NoLeadHasRun {
		t.Fatal("the sample no longer states that no lead has run")
	}
	got, err := ReadInputs(writeInputs(t, data, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if got.NoLeadHasRun || got.PreviousInstanceStopped {
		t.Errorf("the file set a per-epoch field: %+v / %+v", got.NoLeadHasRun, got.PreviousInstanceStopped)
	}
}
