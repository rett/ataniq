package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/assignment"
)

func sampleConfig(dir string) map[string]any {
	return map[string]any{
		"state": dir, "home_key": dir + "/seed", "tls_dir": dir + "/tls",
		"listen": "127.0.0.1:7444", "epoch": "1",
		"slot":          map[string]any{"witness": "witness", "authority": "authority", "store": "store"},
		"trust_sources": []string{"127.0.0.1/32"},
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeConfig(t *testing.T, path string, fields map[string]any) {
	t.Helper()
	if err := os.WriteFile(path, encode(t, fields), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFieldErrors(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"epoch", 1}, {"epoch", "0"}, {"epoch", "18446744073709551616"}, {"epoch", "+1"}, {"epoch", "2.0"},
		{"state", "relative"}, {"state", "/"}, {"home_key", "/dir/../seed"}, {"tls_dir", "/dir/"},
		{"binding", nil}, {"pending_journal", ""}, {"pending_journal", "relative"},
		{"listen", "localhost:7444"}, {"listen", "127.0.0.1:01"}, {"listen", "127.0.0.1:0"},
		{"listen", "127.0.0.1:65536"}, {"listen", "[::ffff:127.0.0.1]:7444"},
		{"listen", "[fe80::1%eth0]:7444"}, {"listen", "[0:0::1]:7444"},
		{"slot.witness", ""}, {"slot.authority", "é"}, {"slot.store", "a\tb"},
		{"slot.witness", strings.Repeat("x", assignment.MaxReferenceBytes+1)},
		{"trust_sources", nil}, {"trust_sources", []string{}}, {"trust_sources", []any{nil}},
		{"trust_sources", []string{"127.0.0.1"}}, {"trust_sources", []string{"::/0"}},
		{"trust_sources", []string{"0.0.0.0/0"}}, {"trust_sources", []string{"::ffff:127.0.0.1/96"}},
		{"trust_sources", []string{"::ffff:127.0.0.1/95"}},
		{"witness_cert", nil}, {"witness_cert", ""}, {"witness_cert", "relative"},
		{"witness_public_key", strings.Repeat("F", 64)}, {"witness_tls_pin", "a"},
	} {
		m := sampleConfig("/state")
		if sub, ok := strings.CutPrefix(tc.field, "slot."); ok {
			m["slot"].(map[string]any)[sub] = tc.value
		} else {
			m[tc.field] = tc.value
		}
		_, err := decodeConfig(encode(t, m), identityPhase)
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s = %v: got %v", tc.field, tc.value, err)
		}
	}
}

func TestConfigStrictObjects(t *testing.T) {
	base := string(encode(t, sampleConfig("/state")))
	for _, tc := range []struct{ from, to, field string }{
		{`"listen":`, `"Listen":`, "Listen"}, {`"state":`, `"extra":0,"state":`, "extra"},
		{`"state":`, `"stat\u0065":"first","state":`, "state"},
		{`"witness":`, `"witness":"first","witness":`, "witness"},
		{`"store":`, `"Store":`, "Store"}, {`"epoch":"1",`, "", "epoch"},
		{`"authority":"authority",`, "", "authority"},
	} {
		b := strings.Replace(base, tc.from, tc.to, 1)

		_, err := decodeConfig([]byte(b), identityPhase)
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: %v", tc.field, err)
		}
	}
}

func TestConfigTrailingData(t *testing.T) {
	b := append(encode(t, sampleConfig("/state")), []byte(" {}")...)
	if _, err := decodeConfig(b, identityPhase); err == nil {
		t.Fatal("accepted trailing object")
	}
}

func TestConfigPhases(t *testing.T) {
	m := sampleConfig("/state")
	m["epoch"] = "18446744073709551615"
	m["slot"].(map[string]any)["authority"] = " ~ "
	m["trust_sources"] = []string{"::ffff:127.0.0.1/97", "::1/128"}
	m["binding"] = "/config/binding"
	m["pending_journal"] = "/config/pending"
	for _, listen := range []string{"[::1]:1", "127.0.0.1:65535"} {
		m["listen"] = listen
		if _, err := decodeConfig(encode(t, m), identityPhase); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"witness_cert": "/cert.der", "witness_tls_pin": strings.Repeat("1", 64), "witness_public_key": strings.Repeat("2", 64)} {
		m[k] = v
	}
	if _, err := decodeConfig(encode(t, m), fullPhase); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"witness_cert", "witness_tls_pin", "witness_public_key"} {
		value := m[key]
		delete(m, key)
		if _, err := decodeConfig(encode(t, m), fullPhase); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("missing %s: %v", key, err)
		}
		if _, err := decodeConfig(encode(t, m), identityPhase); err != nil {
			t.Fatal(err)
		}
		m[key] = value
	}
}

func TestConfigPinnedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	writeConfig(t, cfg, sampleConfig(dir))
	if _, err := loadConfig(cfg, identityPhase); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(cfg, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(alias, identityPhase); err == nil {
		t.Fatal("accepted linked config")
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(cfg, identityPhase); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("public state: %v", err)
	}
}
