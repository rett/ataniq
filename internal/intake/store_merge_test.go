//go:build linux

package intake

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/intake/verify"
)

// TestPendingPrefixesAreEqual: the verify package's prefix for pins is the
// intake's.
func TestPendingPrefixesAreEqual(t *testing.T) {
	if verify.PendingPrefix != PendingPrefix {
		t.Errorf("verify.PendingPrefix = %q, PendingPrefix = %q", verify.PendingPrefix, PendingPrefix)
	}
}

// TestPutVerificationRefByMode: a lead-mode result takes a lead ref only and a
// merge-mode result a pin only; each refusal writes nothing.
func TestPutVerificationRefByMode(t *testing.T) {
	tip, base := idOf('a'), idOf('b')
	lead := passResult(tip)
	merge := mergeResult(tip, base)
	pinInLead := passResult(tip)
	pinInLead.TipRef = testPin
	leadInMerge := mergeResult(tip, base)
	leadInMerge.TipRef = leadRef
	mergeNoBase := mergeResult(tip, "")
	mergeShortBase := mergeResult(tip, strings.Repeat("b", 64))
	leadWithBase := passResult(tip)
	leadWithBase.Base = base
	bare := mergeResult(tip, base)
	bare.TipRef = PendingPrefix

	for _, c := range []struct {
		name string
		res  verify.Result
		ok   bool
		is   error
	}{
		{"lead ref, lead mode", lead, true, nil},
		{"pin, merge mode", merge, true, nil},
		{"pin, lead mode", pinInLead, false, ErrRefName},
		{"lead ref, merge mode", leadInMerge, false, ErrRefName},
		{"the prefix alone, merge mode", bare, false, ErrRefName},
		{"merge mode with no base", mergeNoBase, false, ErrStore},
		{"merge mode, base of another format", mergeShortBase, false, ErrStore},
		{"lead mode with a base", leadWithBase, false, ErrStore},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, dir := newTestStore(t)
			rec, err := st.PutVerification(testProject, c.res, testGates)
			if c.ok {
				if err != nil {
					t.Fatalf("PutVerification: %v", err)
				}
				if rec.Merge != c.res.Merge || rec.Base != c.res.Base || rec.TipRef != c.res.TipRef {
					t.Errorf("record: %+v", rec)
				}
				return
			}
			if !errors.Is(err, c.is) {
				t.Errorf("error = %v, want %v", err, c.is)
			}
			if got := mustReadDir(t, dir); len(got) != 0 {
				t.Errorf("files written: %v", got)
			}
		})
	}
}

// TestVerificationRecordMergeFieldsRoundTrip: a merge record keeps its merge
// flag and base through the file, the digest-bearing JSON names them, and a
// lead-mode record is written without either key, as before.
func TestVerificationRecordMergeFieldsRoundTrip(t *testing.T) {
	st, dir := newTestStore(t)
	tip, base := idOf('a'), idOf('b')
	if _, err := st.PutVerification(testProject, mergeResult(tip, base), testGates); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutVerification(testProject, passResult(tip), testGates); err != nil {
		t.Fatal(err)
	}
	all, err := st.Verifications(tip)
	if err != nil || len(all) != 2 {
		t.Fatalf("%v, %d records", err, len(all))
	}
	if !all[0].Merge || all[0].Base != base || all[0].TipRef != testPin {
		t.Errorf("merge record: %+v", all[0])
	}
	if all[1].Merge || all[1].Base != "" || all[1].TipRef != leadRef {
		t.Errorf("lead record: %+v", all[1])
	}
	var texts []string
	for _, name := range mustReadDir(t, dir) {
		if strings.HasSuffix(name, ".json") {
			texts = append(texts, mustRead(t, filepath.Join(dir, name)))
		}
	}
	if len(texts) != 2 {
		t.Fatalf("%d record files", len(texts))
	}
	if !strings.Contains(texts[0], `"merge": true`) || !strings.Contains(texts[0], `"base": "`+base+`"`) {
		t.Errorf("merge record text:\n%s", texts[0])
	}
	if strings.Contains(texts[1], `"merge"`) || strings.Contains(texts[1], `"base"`) {
		t.Errorf("lead record text names the merge fields:\n%s", texts[1])
	}
}

// TestVerificationRecordOldLeadRecordStillReads: a record written before the
// merge fields existed has no such keys and reads as a lead-mode record.
func TestVerificationRecordOldLeadRecordStillReads(t *testing.T) {
	old := `{"version":1,"project":"ataniq","tip_ref":"refs/leads/lead/heads/main","tip":"` + idOf('a') +
		`","plan_digest":"` + verify.PlanDigest(testGates) + `","gates":[{"name":"vet","argv":["make","vet"]}],` +
		`"passed":true,"exit":0,"stopped":"","output_sha256":"` + strings.Repeat("0", 64) +
		`","output_bytes":0,"output_cut":false,"started":"2026-11-17T09:00:00Z","duration_ms":1}`
	var rec VerificationRecord
	if err := json.Unmarshal([]byte(old), &rec); err != nil {
		t.Fatalf("an old record: %v", err)
	}
	if rec.Merge || rec.Base != "" || !rec.passing() {
		t.Errorf("record: %+v", rec)
	}
	// A record that says merge without a base, or a base without merge, is
	// malformed.
	for name, text := range map[string]string{
		"merge without base": strings.Replace(old, `"tip":`, `"merge":true,"tip":`, 1),
		"base without merge": strings.Replace(old, `"tip":`, `"base":"`+idOf('b')+`","tip":`, 1),
	} {
		if err := json.Unmarshal([]byte(text), &VerificationRecord{}); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	good := strings.Replace(old, `"tip":`, `"merge":true,"base":"`+idOf('b')+`","tip":`, 1)
	var m VerificationRecord
	if err := json.Unmarshal([]byte(good), &m); err != nil || !m.Merge || m.Base != idOf('b') {
		t.Errorf("a merge record: %v %+v", err, m)
	}
}
