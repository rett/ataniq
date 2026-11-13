//go:build linux && amd64

package qualification

import (
	"bytes"
	"debug/elf"
	"errors"
	"strings"
)

// Read the loader's program-header view, never SHT_DYNAMIC/DynString. Section
// headers may be absent or disagree with the runtime tables. This is a bounded
// metadata check, not a loader or a claim about dynamic dependency closure.
func launcherELF(e *elf.File) (ELFMetadata, error) {
	m := ELFMetadata{}
	if e.Class != elf.ELFCLASS64 || e.Data != elf.ELFDATA2LSB || e.Machine != elf.EM_X86_64 || (e.Type != elf.ET_EXEC && e.Type != elf.ET_DYN) || len(e.Progs) > 128 {
		return m, errors.New("unsupported: Linux ELF64 amd64 launch subject required")
	}
	var dynamic *elf.Prog
	for _, p := range e.Progs {
		if p.Filesz > p.Memsz || p.Vaddr > ^uint64(0)-p.Memsz || p.Off > (1<<63-1)-p.Filesz || p.Filesz > 64<<20 {
			return m, errors.New("unsupported: invalid ELF segment bounds")
		}
		switch p.Type {
		case elf.PT_INTERP:
			if m.Interpreter != "" || p.Filesz < 2 || p.Filesz > 4096 {
				return m, errors.New("unsupported: invalid or multiple interpreters")
			}
			b := make([]byte, int(p.Filesz))
			if _, err := p.ReadAt(b, 0); err != nil || b[len(b)-1] != 0 {
				return m, errors.New("unsupported: invalid interpreter")
			}
			m.Interpreter = string(b[:len(b)-1])
			if !absolute(m.Interpreter) {
				return m, errors.New("unsupported: nonabsolute interpreter")
			}
		case elf.PT_DYNAMIC:
			if dynamic != nil || p.Filesz == 0 || p.Filesz > 65536 || p.Filesz%16 != 0 {
				return m, errors.New("unsupported: invalid or multiple dynamic tables")
			}
			dynamic = p
		}
	}
	if dynamic == nil {
		m.DependencyClosureComplete = m.Interpreter == ""
		return m, nil
	}
	// The loader reads the dynamic table at its mapped virtual address. Require
	// the file extent and PT_DYNAMIC offset to describe those same bytes.
	load, offset, err := runtimeExtent(e, dynamic.Vaddr, dynamic.Filesz)
	if err != nil || load.Off+offset != dynamic.Off {
		return m, errors.New("unsupported: unmapped or inconsistent dynamic table")
	}
	b := make([]byte, int(dynamic.Filesz))
	if _, err = load.ReadAt(b, int64(offset)); err != nil {
		return m, errors.New("unsupported: truncated dynamic table")
	}
	var strtab, strsz uint64
	var haveTable, haveSize, terminated bool
	var needed []uint64
	for len(b) != 0 {
		tag := elf.DynTag(e.ByteOrder.Uint64(b[:8]))
		value := e.ByteOrder.Uint64(b[8:16])
		b = b[16:]
		if tag == elf.DT_NULL {
			terminated = true
			break
		}
		switch tag {
		case elf.DT_RPATH, elf.DT_RUNPATH:
			// Presence alone is forbidden, including empty or invalid strings.
			return m, errors.New("unsupported: bwrap RPATH or RUNPATH")
		case elf.DT_NEEDED:
			needed = append(needed, value)
			if len(needed) > 32 {
				return m, errors.New("unsupported: excessive launcher dependencies")
			}
		case elf.DT_STRTAB:
			if haveTable {
				return m, errors.New("unsupported: duplicate runtime string table")
			}
			strtab, haveTable = value, true
		case elf.DT_STRSZ:
			if haveSize {
				return m, errors.New("unsupported: duplicate runtime string size")
			}
			strsz, haveSize = value, true
		}
	}
	if !terminated || haveTable != haveSize || len(needed) != 0 && !haveTable {
		return m, errors.New("unsupported: incomplete runtime dynamic metadata")
	}
	var stringsTable []byte
	if haveTable {
		if strsz == 0 || strsz > 65536 {
			return m, errors.New("unsupported: invalid runtime string size")
		}
		load, offset, err = runtimeExtent(e, strtab, strsz)
		if err != nil {
			return m, err
		}
		stringsTable = make([]byte, int(strsz))
		if _, err = load.ReadAt(stringsTable, int64(offset)); err != nil {
			return m, errors.New("unsupported: truncated runtime strings")
		}
	}
	names := make([]string, 0, len(needed))
	for _, index := range needed {
		if index >= uint64(len(stringsTable)) {
			return m, errors.New("unsupported: invalid dependency string index")
		}
		rest := stringsTable[index:]
		end := bytes.IndexByte(rest, 0)
		if end < 0 || !bounded(string(rest[:end]), 1, 128, isName) {
			return m, errors.New("unsupported: invalid or path-valued launcher dependency")
		}
		names = append(names, string(rest[:end]))
	}
	m.Needed = strings.Join(names, ",")
	m.DependencyClosureComplete = m.Interpreter == "" && len(needed) == 0
	return m, nil
}

// Require exactly one file-backed PT_LOAD extent, excluding overlapping memory
// mappings (including zero-fill) that could change the bytes seen by the loader.
func runtimeExtent(e *elf.File, address, size uint64) (*elf.Prog, uint64, error) {
	if size == 0 || address > ^uint64(0)-size {
		return nil, 0, errors.New("unsupported: invalid runtime extent")
	}
	var found *elf.Prog
	var offset uint64
	for _, p := range e.Progs {
		if p.Type != elf.PT_LOAD || address >= p.Vaddr+p.Memsz || p.Vaddr >= address+size {
			continue
		}
		if found != nil || address < p.Vaddr || address-p.Vaddr > p.Filesz || size > p.Filesz-(address-p.Vaddr) {
			return nil, 0, errors.New("unsupported: ambiguous or non-file-backed runtime extent")
		}
		found, offset = p, address-p.Vaddr
	}
	if found == nil {
		return nil, 0, errors.New("unsupported: unmapped runtime extent")
	}
	return found, offset, nil
}
