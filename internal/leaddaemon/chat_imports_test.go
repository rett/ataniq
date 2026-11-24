package leaddaemon

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// chatFile reports whether name is a file of the scratch leads' chat, a
// source file or one of its tests.
func chatFile(name string) bool {
	return strings.HasPrefix(name, "chat_") && (strings.HasSuffix(name, "_linux.go") || strings.HasSuffix(name, "_linux_test.go"))
}

// chatImportsAllowed are the only packages of the module each source file of
// the chat may import. The seat's port reaches nothing of the module but the
// wire's types: no lifecycle, holder, unit manager, injector, command table,
// provisioning, and of the approval packages nothing but ownerwire (no owner
// service, decide path, pairing, key list, limits, intake or host effects).
// The desk adds the chat store, the registry it reads the leads' states from,
// the role names, the client protocol and the wire of its two operations; the
// desk's command, the client protocol and that wire alone.
var chatImportsAllowed = map[string]map[string]bool{
	"chat_port_linux.go": {
		"ataniq/internal/approval/ownerwire": true,
	},
	"chat_cli_linux.go": {
		"ataniq/internal/clientapi": true,
		"ataniq/internal/leadwire":  true,
	},
	"chat_desk_linux.go": {
		"ataniq/internal/approval/ownerwire": true,
		"ataniq/internal/clientapi":          true,
		"ataniq/internal/leadchat":           true,
		"ataniq/internal/leadregistry":       true,
		"ataniq/internal/leadrole":           true,
		"ataniq/internal/leadwire":           true,
	},
}

// TestChatFilesImportOnlyWhatTheyRead: every source file of the chat carries
// the canary constraint and is one of the files above, and imports nothing of
// the module beyond its list. This test holds in a build with the tag and in
// one without it.
func TestChatFilesImportOnlyWhatTheyRead(t *testing.T) {
	files, err := filepath.Glob("chat_*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		seen++
		allowed, known := chatImportsAllowed[name]
		if !known || !chatFile(name) {
			t.Errorf("%s is a chat file the guard does not know", name)
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if first, _, _ := strings.Cut(string(src), "\n"); first != "//go:build linux && ataniq_canary" {
			t.Errorf("%s starts with %q", name, first)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if (p == modulePath || strings.HasPrefix(p, modulePath+"/")) && !allowed[p] {
				t.Errorf("%s imports %s", name, p)
			}
			// The port and the desk reach no process, network, file or raw
			// system call of their own: the chat store does the files.
			if name != "chat_cli_linux.go" && (p == "os" || p == "os/exec" || p == "net" || p == "unsafe" || p == "syscall") {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if seen != len(chatImportsAllowed) {
		t.Fatalf("read %d chat files, the guard knows %d", seen, len(chatImportsAllowed))
	}
}
