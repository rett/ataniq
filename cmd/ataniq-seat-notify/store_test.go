package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/seatnotice"
)

func allowAll(string) bool { return true }

// sub makes a valid subscription for the store's tests; they need no push
// service.
func sub(name string) seatnotice.Subscription {
	p := make([]byte, 65)
	p[0] = 0x04
	for i := 1; i < len(p); i++ {
		p[i] = byte(i)
	}
	return seatnotice.Subscription{
		Endpoint: "https://push.example/send/" + name,
		P256DH:   b64(p),
		Auth:     b64([]byte("0123456789abcdef")),
	}
}

func TestStoreWritesAndReadsBack(t *testing.T) {
	st, path := testStore(t, allowAll)
	if st.count() != 0 {
		t.Fatal("a new store is not empty")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("opening a store made a file")
	}
	for _, n := range []string{"a", "b", "c"} {
		if err := st.put(sub(n)); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("the file: %v %v", fi, err)
	}
	// No temporary file is left beside it.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("the folder holds %d entries", len(entries))
	}
	data, _ := os.ReadFile(path)
	want := `{"version":1,"subscriptions":[{"endpoint":"https://push.example/send/a"`
	if !strings.HasPrefix(string(data), want) || !strings.HasSuffix(string(data), "}]}\n") {
		t.Errorf("the file: %s", data)
	}
	again, err := openStore(path, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.list(); len(got) != 3 || got[0] != sub("a") || got[2] != sub("c") {
		t.Errorf("read back %v", got)
	}
	n, err := again.remove(seatnotice.EndpointSHA256(sub("b").Endpoint), seatnotice.EndpointSHA256("https://x.example/"))
	if n != 1 || err != nil {
		t.Fatalf("remove: %d %v", n, err)
	}
	third, err := openStore(path, allowAll)
	if err != nil || third.count() != 2 {
		t.Fatalf("after remove: %v %v", third, err)
	}
	if n, err := again.remove("0000000000000000000000000000000000000000000000000000000000000000"); n != 0 || err != nil {
		t.Fatalf("remove of nothing: %d %v", n, err)
	}
}

func TestStoreAtMostFourAndReplace(t *testing.T) {
	st, _ := testStore(t, allowAll)
	for _, n := range []string{"1", "2", "3", "4"} {
		if err := st.put(sub(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.put(sub("5")); err != errTooMany {
		t.Fatalf("a fifth: %v", err)
	}
	if st.count() != 4 {
		t.Fatal("a fifth was kept")
	}
	renewed := sub("2")
	renewed.Auth = b64([]byte("fedcba9876543210"))
	if err := st.put(renewed); err != nil {
		t.Fatal(err)
	}
	got := st.list()
	if len(got) != 4 || got[1] != renewed || got[0] != sub("1") {
		t.Errorf("replace: %v", got)
	}
	// list is a copy.
	got[0].Endpoint = "https://changed.example/"
	if st.list()[0] != sub("1") {
		t.Error("list gave the store's own slice")
	}
}

// TestStoreWriteFailureChangesNothing: a write that cannot be made leaves
// the file and the held subscriptions as they were.
func TestStoreWriteFailureChangesNothing(t *testing.T) {
	st, path := testStore(t, allowAll)
	if err := st.put(sub("a")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		f.Close()
		t.Skip("a read-only folder can still be written (running as root)")
	}
	if err := st.put(sub("b")); err == nil {
		t.Fatal("a put into an unwritable folder succeeded")
	}
	if _, err := st.remove(seatnotice.EndpointSHA256(sub("a").Endpoint)); err == nil {
		t.Fatal("a remove from an unwritable folder succeeded")
	}
	os.Chmod(dir, 0o700)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || st.count() != 1 {
		t.Errorf("the store changed: %d held", st.count())
	}
}

func TestStoreRemovesAStaleTemporaryFile(t *testing.T) {
	dir := ownDir(t)
	path := filepath.Join(dir, "subscriptions.json")
	stale := filepath.Join(dir, ".subscriptions.json-12345.tmp")
	other := filepath.Join(dir, ".other-1.tmp")
	os.WriteFile(stale, []byte("half"), 0o600)
	os.WriteFile(other, []byte("not ours"), 0o600)
	if _, err := openStore(path, allowAll); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale temporary file was left")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file that is not the store's was removed")
	}
}

func goodJSON(subs ...seatnotice.Subscription) string {
	var parts []string
	for _, s := range subs {
		parts = append(parts, `{"endpoint":"`+s.Endpoint+`","p256dh":"`+s.P256DH+`","auth":"`+s.Auth+`"}`)
	}
	return `{"version":1,"subscriptions":[` + strings.Join(parts, ",") + `]}` + "\n"
}

func TestStoreReadsStrictly(t *testing.T) {
	a, b := sub("a"), sub("b")
	one := goodJSON(a)
	obj := func(s seatnotice.Subscription) string {
		return `{"endpoint":"` + s.Endpoint + `","p256dh":"` + s.P256DH + `","auth":"` + s.Auth + `"}`
	}
	cases := map[string]string{
		"empty":               ``,
		"not an object":       `[]`,
		"trailing data":       one + `{}`,
		"trailing garbage":    strings.TrimSpace(one) + `x`,
		"two objects":         one + one,
		"no version":          `{"subscriptions":[]}`,
		"no subscriptions":    `{"version":1}`,
		"version 2":           `{"version":2,"subscriptions":[]}`,
		"version as string":   `{"version":"1","subscriptions":[]}`,
		"version 1.0":         `{"version":1.0,"subscriptions":[]}`,
		"unknown key":         `{"version":1,"subscriptions":[],"extra":1}`,
		"repeated key":        `{"version":1,"version":1,"subscriptions":[]}`,
		"repeated list":       `{"version":1,"subscriptions":[],"subscriptions":[]}`,
		"wrong case":          `{"Version":1,"subscriptions":[]}`,
		"wrong case inside":   `{"version":1,"subscriptions":[{"Endpoint":"` + a.Endpoint + `","p256dh":"` + a.P256DH + `","auth":"` + a.Auth + `"}]}`,
		"unknown sub key":     `{"version":1,"subscriptions":[{"endpoint":"` + a.Endpoint + `","p256dh":"` + a.P256DH + `","auth":"` + a.Auth + `","x":"y"}]}`,
		"repeated sub key":    `{"version":1,"subscriptions":[{"endpoint":"` + a.Endpoint + `","endpoint":"` + a.Endpoint + `","p256dh":"` + a.P256DH + `","auth":"` + a.Auth + `"}]}`,
		"missing sub key":     `{"version":1,"subscriptions":[{"endpoint":"` + a.Endpoint + `","p256dh":"` + a.P256DH + `"}]}`,
		"null subscriptions":  `{"version":1,"subscriptions":null}`,
		"null value":          `{"version":1,"subscriptions":[{"endpoint":null,"p256dh":"` + a.P256DH + `","auth":"` + a.Auth + `"}]}`,
		"number value":        `{"version":1,"subscriptions":[{"endpoint":"` + a.Endpoint + `","p256dh":1,"auth":"` + a.Auth + `"}]}`,
		"a string in a list":  `{"version":1,"subscriptions":["x"]}`,
		"five subscriptions":  `{"version":1,"subscriptions":[` + strings.Join([]string{obj(sub("1")), obj(sub("2")), obj(sub("3")), obj(sub("4")), obj(sub("5"))}, ",") + `]}`,
		"the same endpoint":   goodJSON(a, a),
		"endpoint not https":  strings.Replace(one, "https://", "http://", 1),
		"endpoint not listed": strings.Replace(one, "push.example", "other.example", 1),
		"short key":           strings.Replace(one, a.Auth, "AAAA", 1),
		"invalid UTF-8":       "{\"version\":1,\"subscriptions\":[],\"\xff\":1}",
	}
	allow := func(e string) bool { return strings.HasPrefix(e, "https://push.example/") }
	// A control file that the strict reader takes, so a case that fails does
	// not fail for another reason.
	dir := ownDir(t)
	path := filepath.Join(dir, "subscriptions.json")
	if err := os.WriteFile(path, []byte(goodJSON(a, b)), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := openStore(path, allow); err != nil || st.count() != 2 {
		t.Fatalf("the control file: %v", err)
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := ownDir(t)
			path := filepath.Join(dir, "subscriptions.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := openStore(path, allow); err == nil {
				t.Fatalf("%q was read", content)
			} else if strings.Contains(err.Error(), "push.example") || strings.Contains(err.Error(), dir) {
				t.Fatalf("the error names a path or an endpoint: %v", err)
			}
			// The file is left as it was: never repaired.
			if got, _ := os.ReadFile(path); string(got) != content {
				t.Error("the file was changed")
			}
		})
	}
}

func TestStoreRefusesAnUnsafeFileOrFolder(t *testing.T) {
	good := goodJSON(sub("a"))
	setup := func(t *testing.T) (dir, path string) {
		dir = ownDir(t)
		path = filepath.Join(dir, "subscriptions.json")
		if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, path
	}
	t.Run("mode 0644", func(t *testing.T) {
		_, path := setup(t)
		os.Chmod(path, 0o644)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("mode 0640", func(t *testing.T) {
		_, path := setup(t)
		os.Chmod(path, 0o640)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("mode 0400", func(t *testing.T) {
		_, path := setup(t)
		os.Chmod(path, 0o400)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a symbolic link", func(t *testing.T) {
		dir, path := setup(t)
		real := filepath.Join(dir, "real.json")
		os.Rename(path, real)
		os.Symlink(real, path)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a hard link", func(t *testing.T) {
		dir, path := setup(t)
		os.Link(path, filepath.Join(dir, "second.json"))
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a folder", func(t *testing.T) {
		dir := ownDir(t)
		path := filepath.Join(dir, "subscriptions.json")
		os.Mkdir(path, 0o700)
		if _, err := openStore(path, allowAll); err == nil {
			t.Fatal("a folder was read")
		}
	})
	t.Run("too large", func(t *testing.T) {
		_, path := setup(t)
		os.WriteFile(path, []byte(good+strings.Repeat(" ", maxStoreFile)), 0o600)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a folder others can write", func(t *testing.T) {
		dir, path := setup(t)
		os.Chmod(dir, 0o770)
		if _, err := openStore(path, allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a link for a folder", func(t *testing.T) {
		dir, _ := setup(t)
		link := filepath.Join(ownDir(t), "link")
		os.Symlink(dir, link)
		if _, err := openStore(filepath.Join(link, "subscriptions.json"), allowAll); err != errStoreUnsafe {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no folder", func(t *testing.T) {
		if _, err := openStore(filepath.Join(ownDir(t), "nope", "s.json"), allowAll); err == nil {
			t.Fatal("a missing folder was taken")
		}
	})
	t.Run("a good file", func(t *testing.T) {
		_, path := setup(t)
		if st, err := openStore(path, allowAll); err != nil || st.count() != 1 {
			t.Fatalf("%v", err)
		}
	})
}
