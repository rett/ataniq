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

// Separate the dynamic and string tables onto two loads/pages. The third load
// is logically disjoint from both, but can remap either metadata page from a
// different file page. These bytes are inspected only, never executed.
func pageMappingELF(t *testing.T, overlap string) *elf.File {
	t.Helper()
	page := uint64(os.Getpagesize())
	const base = 0x400000
	b := make([]byte, 4*page)
	copy(b, launcherBytes("", elf.DT_NEEDED, "libc.so.6"))
	stripELFSections(b)
	binary.LittleEndian.PutUint16(b[56:], 4)
	putELF64(b, 96, 0x500)
	putELF64(b, 104, 0x500)
	putELF64(b, 520, base+page+0x280)
	copy(b[page+0x280:], b[640:651])
	load := func(header int, offset, address uint64) {
		binary.LittleEndian.PutUint32(b[header:], uint32(elf.PT_LOAD))
		putELF64(b, header+8, offset)
		putELF64(b, header+16, address)
		putELF64(b, header+32, 0x100)
		putELF64(b, header+40, 0x100)
		putELF64(b, header+48, page)
	}
	load(232, page, base+page)
	putELF64(b, 232+32, 0x500)
	putELF64(b, 232+40, 0x500)
	// Boundary positive: the later mapping begins exactly after the string page.
	load(120, 3*page, base+2*page)
	switch overlap {
	case "dynamic":
		load(120, 3*page+0x800, base+0x800)
		// The kernel would expose RUNPATH where the reader saw STRTAB.
		copy(b[3*page+512:], b[512:576])
		putELF64(b, int(3*page+512), uint64(elf.DT_RUNPATH))
	case "strings":
		load(120, 3*page+0x800, base+page+0x800)
		copy(b[3*page+0x280:], "./evil.so\x00")
	}
	e, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRuntimeMetadataPageMapping(t *testing.T) {
	for _, extent := range []string{"dynamic", "strings", "boundary"} {
		t.Run(extent, func(t *testing.T) {
			m, err := launcherELF(pageMappingELF(t, extent))
			if extent == "boundary" {
				if err != nil || m.Needed != "libc.so.6" || m.DependencyClosureComplete {
					t.Fatal("adjacent mapping rejected or metadata changed", m, err)
				}
				return
			}
			if err == nil {
				t.Fatal("same-page mapping from a different file page accepted", m)
			}
			// The strings fixture must reach DT_STRTAB validation: its dynamic
			// table is on a separate, unambiguous page.
			if extent == "strings" && !strings.Contains(err.Error(), "runtime extent") {
				t.Fatal("string-table overlap not isolated", err)
			}
		})
	}
}

func TestRuntimeMetadataPageBounds(t *testing.T) {
	page := uint64(os.Getpagesize())
	// Linux/amd64 ELF_MIN_ALIGN and the host base-page size must agree. Runtime
	// checks use the actual host size, not p_align or a copied 4096 constant.
	if page != 4096 {
		t.Fatalf("unverified Linux/amd64 ELF page-size premise: %d", page)
	}
	lastPageEnd := ^uint64(0) &^ (page - 1)
	for _, tc := range []struct {
		name                  string
		address, size         uint64
		loadAddress, loadSize uint64
		ok                    bool
	}{
		{"zero-extent", 0, 0, 0, page, false},
		{"extent-add-overflow", ^uint64(0) - 15, 32, 0, page, false},
		{"extent-round-overflow", lastPageEnd + 1, 1, lastPageEnd, page - 1, false},
		{"load-add-overflow", lastPageEnd, 16, lastPageEnd, page, false},
		{"load-round-overflow", lastPageEnd, 16, lastPageEnd, page - 1, false},
		{"last-representable-end", lastPageEnd - 16, 16, lastPageEnd - page, page, true},
		{"exact-page-end", page - 16, 16, 0, page, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &elf.Prog{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Vaddr: tc.loadAddress, Filesz: tc.loadSize, Memsz: tc.loadSize}}
			e := &elf.File{Progs: []*elf.Prog{p}}
			load, offset, err := runtimeExtent(e, tc.address, tc.size)
			if (err == nil) != tc.ok || tc.ok && (load != p || offset != tc.address-tc.loadAddress) {
				t.Fatal("unsafe page bounds or wrong file extent", load, offset, err)
			}
		})
	}
	// An extent ending exactly on a page boundary does not touch the next load.
	e := &elf.File{Progs: []*elf.Prog{
		{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Filesz: page, Memsz: page}},
		{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Vaddr: page, Filesz: page, Memsz: page}},
	}}
	if _, _, err := runtimeExtent(e, page-16, 16); err != nil {
		t.Fatal("nonoverlapping page boundary rejected", err)
	}
}
