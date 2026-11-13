//go:build linux && amd64

package qualification

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putELF64(b []byte, offset int, value uint64) { binary.LittleEndian.PutUint64(b[offset:], value) }

func stripELFSections(b []byte) {
	putELF64(b, 40, 0)
	binary.LittleEndian.PutUint16(b[60:], 0)
}

func TestLauncherRuntimeMetadataIgnoresSectionView(t *testing.T) {
	for _, view := range []string{"congruent", "absent", "mismatched"} {
		for _, tag := range []elf.DynTag{elf.DT_NEEDED, elf.DT_RPATH, elf.DT_RUNPATH} {
			t.Run(view+"-"+tag.String(), func(t *testing.T) {
				b := launcherBytes("/lib/loader", tag, "libc.so.6")
				switch view {
				case "absent":
					stripELFSections(b)
				case "mismatched":
					// SHT_DYNAMIC now describes an empty table; PT_DYNAMIC is intact.
					putELF64(b, 768+64+24, 704)
				}
				e, err := elf.NewFile(bytes.NewReader(b))
				if err != nil {
					t.Fatal(err)
				}
				m, err := launcherELF(e)
				if tag == elf.DT_NEEDED {
					if err != nil || m.Interpreter != "/lib/loader" || m.Needed != "libc.so.6" || m.DependencyClosureComplete {
						t.Fatal("runtime dependency metadata lost", m, err)
					}
				} else if err == nil {
					t.Fatal("runtime search tag hidden by section view")
				}
			})
		}
	}
}

func TestLauncherMalformedRuntimeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]byte)
	}{
		{"needed-outside-table", func(b []byte) { putELF64(b, 552, 99) }},
		{"needed-overflow-index", func(b []byte) { putELF64(b, 552, ^uint64(0)) }},
		{"needed-empty", func(b []byte) { putELF64(b, 552, 0) }},
		{"needed-unterminated", func(b []byte) { b[650] = 'x' }},
		{"needed-path", func(b []byte) { copy(b[641:], "./libc.so") }},
		{"needed-absolute", func(b []byte) { b[641] = '/' }},
		{"missing-strtab", func(b []byte) { putELF64(b, 512, uint64(elf.DT_DEBUG)) }},
		{"missing-strsz", func(b []byte) { putELF64(b, 528, uint64(elf.DT_DEBUG)) }},
		{"duplicate-strtab", func(b []byte) { putELF64(b, 544, uint64(elf.DT_STRTAB)) }},
		{"duplicate-strsz", func(b []byte) { putELF64(b, 544, uint64(elf.DT_STRSZ)) }},
		{"oversize-strings", func(b []byte) { putELF64(b, 536, 65537) }},
		{"zero-strings", func(b []byte) { putELF64(b, 536, 0) }},
		{"unmapped-strings", func(b []byte) { putELF64(b, 520, 0x500000) }},
		{"overflow-strings", func(b []byte) { putELF64(b, 520, ^uint64(0)-1) }},
		{"zero-fill-strings", func(b []byte) { putELF64(b, 96, 640) }},
		{"partial-strings", func(b []byte) { putELF64(b, 96, 645) }},
		{"ambiguous-load", func(b []byte) { copy(b[120:176], b[64:120]) }},
		{"dynamic-offset-mismatch", func(b []byte) { putELF64(b, 184, 704) }},
		{"unmapped-dynamic", func(b []byte) { putELF64(b, 192, 0x500000) }},
		{"unaligned-dynamic-size", func(b []byte) { putELF64(b, 208, 63) }},
		{"oversize-dynamic", func(b []byte) { putELF64(b, 208, 65552); putELF64(b, 216, 65552) }},
		{"multiple-dynamic", func(b []byte) { copy(b[120:176], b[176:232]) }},
		{"unterminated-dynamic", func(b []byte) { putELF64(b, 560, uint64(elf.DT_DEBUG)) }},
		{"overflow-load", func(b []byte) { putELF64(b, 80, ^uint64(0)-1) }},
		{"overflow-file-extent", func(b []byte) { putELF64(b, 72, (1<<63)-10) }},
		{"filesz-over-memsz", func(b []byte) { putELF64(b, 104, 1) }},
		{"empty-rpath", func(b []byte) { putELF64(b, 544, uint64(elf.DT_RPATH)); putELF64(b, 552, 0) }},
		{"invalid-runpath-index", func(b []byte) { putELF64(b, 544, uint64(elf.DT_RUNPATH)); putELF64(b, 552, ^uint64(0)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := launcherBytes("/lib/loader", elf.DT_NEEDED, "libc.so.6")
			stripELFSections(b)
			tc.change(b)
			e, err := elf.NewFile(bytes.NewReader(b))
			if err != nil {
				t.Fatal("fixture must reach runtime metadata check", err)
			}
			if _, err = launcherELF(e); err == nil {
				t.Fatal("malformed runtime metadata accepted")
			}
		})
	}
}

func TestLauncherRuntimeReadBounds(t *testing.T) {
	for _, size := range []int{550, 645} {
		b := launcherBytes("/lib/loader", elf.DT_NEEDED, "libc.so.6")
		stripELFSections(b)
		e, err := elf.NewFile(bytes.NewReader(b[:size]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = launcherELF(e); err == nil {
			t.Fatal("truncated runtime bytes accepted", size)
		}
	}
	for _, count := range []int{2, 33} {
		b := launcherBytes("/lib/loader", elf.DT_NEEDED, "libc.so.6")
		stripELFSections(b)
		// Enlarge the dynamic table and move the strings after it.
		b = append(b, make([]byte, 1024)...)
		clear(b[512:])
		putELF64(b, 96, uint64(len(b)))
		putELF64(b, 104, uint64(len(b)))
		putELF64(b, 208, uint64((count+3)*16))
		putELF64(b, 216, uint64((count+3)*16))
		putELF64(b, 512, uint64(elf.DT_STRTAB))
		putELF64(b, 520, 0x400600)
		putELF64(b, 528, uint64(elf.DT_STRSZ))
		putELF64(b, 536, 11)
		for n := 0; n < count; n++ {
			putELF64(b, 544+n*16, uint64(elf.DT_NEEDED))
			putELF64(b, 552+n*16, 1)
		}
		copy(b[1537:], "libc.so.6")
		e, err := elf.NewFile(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		m, err := launcherELF(e)
		if count == 2 && (err != nil || m.Needed != "libc.so.6,libc.so.6") || count == 33 && err == nil {
			t.Fatal("dependency count not retained/bounded", count, m, err)
		}
	}
}

func TestRuntimeMetadataSubjectChecksAreShared(t *testing.T) {
	for _, bad := range []bool{false, true} {
		b := launcherBytes("/lib/loader", elf.DT_NEEDED, "libc.so.6")
		stripELFSections(b)
		if bad {
			putELF64(b, 544, uint64(elf.DT_RUNPATH))
		}
		for _, file := range []string{"selected", "frozen", "pre-exec"} {
			p := filepath.Join(t.TempDir(), file)
			if err := os.WriteFile(p, b, 0500); err != nil {
				t.Fatal(err)
			}
			s, _, err := chainSubject("bwrap", p, true)
			if (err != nil) != bad || !bad && (s.ELF.Needed != "libc.so.6" || s.ELF.DependencyClosureComplete) {
				t.Fatal("inconsistent subject metadata", file, s.ELF, err)
			}
		}
	}
	// A section-stripped dependency cannot masquerade as a static driver/probe,
	// even if it omits PT_INTERP. All checks use the same runtime metadata reader.
	for _, name := range []string{"driver", "probe"} {
		p := filepath.Join(t.TempDir(), name)
		b := launcherBytes("", elf.DT_NEEDED, "libc.so.6")
		stripELFSections(b)
		if err := os.WriteFile(p, b, 0500); err != nil {
			t.Fatal(err)
		}
		if _, _, err := chainSubject(name, p, true); err == nil || !strings.Contains(err.Error(), "static") {
			t.Fatal("dynamic subject accepted as static", name, err)
		}
	}
}
