package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// PE export resolution: name -> RVA, read from the file on disk.
//
// This is the step that feeds the SSN resolver, and it carries its own hazard: an RVA computed from the wrong table points at the
// wrong function, so the agent issues a syscall number it read from a stub it was not looking at. Every failure below therefore
// refuses rather than falling back to a plausible number - a forwarded export, a missing name and a truncated table all return an
// error, because "close enough" here means "calls something else on the target".
//
// Like the SSN parser this file is pure: no Windows API, no build tag, so it is exercised by ordinary tests on any platform. Nothing in
// the agent calls it yet.

// PE constants, named rather than inlined because an offset read wrong by four bytes is invisible in review.
const (
	dosHeaderSignature = 0x5A4D     // "MZ"
	peSignature        = 0x00004550 // "PE\0\0"
	dosLfanewOffset    = 0x3C       // e_lfanew: file offset of the PE signature
	pe32Magic          = 0x010B     // optional header magic, 32-bit
	pe32PlusMagic      = 0x020B     // optional header magic, 64-bit
	exportDirectoryIdx = 0          // data directory entry 0 is the export table
	sectionHeaderSize  = 40
	maxExportNameLen   = 512
)

// ErrNotPE reports a buffer that is not a PE image at all.
type ErrNotPE struct{ Reason string }

func (e *ErrNotPE) Error() string { return "not a PE image: " + e.Reason }

// ErrNoExports reports an image with no usable export directory.
type ErrNoExports struct{ Reason string }

func (e *ErrNoExports) Error() string { return "no usable export directory: " + e.Reason }

// ErrExportNotFound reports a name that the export table does not contain.
type ErrExportNotFound struct {
	Name      string
	Available int
}

func (e *ErrExportNotFound) Error() string {
	return fmt.Sprintf("export %q not found (%d name(s) in the table)", e.Name, e.Available)
}

// ErrForwardedExport reports a name that resolves to a forwarder string rather than code.
//
// A forwarder names another module's function; treating its "RVA" as a stub address would read whatever bytes sit in the export
// directory and call it.
type ErrForwardedExport struct {
	Name string
	RVA  uint32
}

func (e *ErrForwardedExport) Error() string {
	return fmt.Sprintf("export %q is forwarded (RVA 0x%X points inside the export directory, not at code)", e.Name, e.RVA)
}

// FindExportRVA returns the RVA of the function body for one exported name.
//
// The image must be the file as read from disk. A mapped image has section-aligned RVAs everywhere and different file offsets; parsing
// the wrong one yields offsets that happen to look plausible, which is exactly the failure this function must not allow.
func FindExportRVA(image []byte, name string) (uint32, error) {
	if len(name) == 0 || len(name) > maxExportNameLen {
		return 0, &ErrExportNotFound{Name: name}
	}
	if len(image) < dosLfanewOffset+4 {
		return 0, &ErrNotPE{Reason: "image is too short to hold a DOS header"}
	}
	if binary.LittleEndian.Uint16(image[0:2]) != dosHeaderSignature {
		return 0, &ErrNotPE{Reason: "missing MZ signature"}
	}
	lfanew := int64(binary.LittleEndian.Uint32(image[dosLfanewOffset : dosLfanewOffset+4]))
	if lfanew <= 0 || lfanew+24 > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "e_lfanew points outside the image"}
	}
	if binary.LittleEndian.Uint32(image[lfanew:lfanew+4]) != peSignature {
		return 0, &ErrNotPE{Reason: "missing PE signature"}
	}

	coff := lfanew + 4
	// COFF header: machine(2) sections(2) timestamp(4) symtab(4) symbols(4) optionalHeaderSize(2) characteristics(2).
	sections := int(binary.LittleEndian.Uint16(image[coff+2 : coff+4]))
	if sections <= 0 {
		return 0, &ErrNotPE{Reason: "no sections"}
	}
	optionalSize := int(binary.LittleEndian.Uint16(image[coff+16 : coff+18]))
	optional := coff + 20
	if optionalSize < 2 || optional+int64(optionalSize) > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "optional header is missing or unreadable"}
	}
	magic := binary.LittleEndian.Uint16(image[optional : optional+2])
	var directoryOffset int
	switch magic {
	case pe32PlusMagic:
		// PE32+: the data directories start at offset 112 of the optional header.
		directoryOffset = 112
	case pe32Magic:
		directoryOffset = 96
	default:
		return 0, &ErrNotPE{Reason: fmt.Sprintf("unknown optional header magic 0x%X", magic)}
	}
	// NumberOfRvaAndSizes sits just before the directories; without it, "entry 0" may be past the header.
	countOffset := directoryOffset - 4
	if countOffset < 0 || optional+int64(countOffset)+4 > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "data directory count is unreadable"}
	}
	countPos := optional + int64(countOffset)
	if binary.LittleEndian.Uint32(image[countPos:countPos+4]) <= exportDirectoryIdx {
		return 0, &ErrNoExports{Reason: "the image declares no data directories"}
	}
	exportEntry := optional + int64(directoryOffset)
	if int64(exportEntry+8) > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "export directory entry is outside the image"}
	}
	exportRVA := binary.LittleEndian.Uint32(image[exportEntry : exportEntry+4])
	exportSize := binary.LittleEndian.Uint32(image[exportEntry+4 : exportEntry+8])
	if exportRVA == 0 || exportSize == 0 {
		return 0, &ErrNoExports{Reason: "the export directory is empty"}
	}

	// Translate the export directory's RVA into a file offset. In a file image the two differ by the section mapping, which is why
	// this walks the section table instead of assuming RVA == offset.
	dirOffset, err := fileOffsetForRVA(image, int64(optional), sections, exportRVA)
	if err != nil {
		return 0, &ErrNoExports{Reason: err.Error()}
	}
	if dirOffset+40 > int64(len(image)) {
		return 0, &ErrNoExports{Reason: "the export directory is truncated"}
	}
	numberOfNames := binary.LittleEndian.Uint32(image[dirOffset+24 : dirOffset+28])
	addressOfFunctions := binary.LittleEndian.Uint32(image[dirOffset+28 : dirOffset+32])
	addressOfNames := binary.LittleEndian.Uint32(image[dirOffset+32 : dirOffset+36])
	addressOfNameOrdinals := binary.LittleEndian.Uint32(image[dirOffset+36 : dirOffset+40])
	if numberOfNames == 0 || addressOfNames == 0 || addressOfNameOrdinals == 0 || addressOfFunctions == 0 {
		return 0, &ErrNoExports{Reason: "the export table has no names"}
	}

	namesOffset, err := fileOffsetForRVA(image, int64(optional), sections, addressOfNames)
	if err != nil {
		return 0, &ErrNoExports{Reason: err.Error()}
	}
	ordinalsOffset, err := fileOffsetForRVA(image, int64(optional), sections, addressOfNameOrdinals)
	if err != nil {
		return 0, &ErrNoExports{Reason: err.Error()}
	}
	functionsOffset, err := fileOffsetForRVA(image, int64(optional), sections, addressOfFunctions)
	if err != nil {
		return 0, &ErrNoExports{Reason: err.Error()}
	}

	for index := uint32(0); index < numberOfNames; index++ {
		nameRVAAt := namesOffset + int64(index)*4
		ordinalAt := ordinalsOffset + int64(index)*2
		if nameRVAAt+4 > int64(len(image)) || ordinalAt+2 > int64(len(image)) {
			return 0, &ErrNoExports{Reason: "the export name table is truncated"}
		}
		nameOffset, err := fileOffsetForRVA(image, int64(optional), sections,
			binary.LittleEndian.Uint32(image[nameRVAAt:nameRVAAt+4]))
		if err != nil {
			continue
		}
		// The name is NUL-terminated; cap the read so a damaged table cannot walk the whole image.
		end := nameOffset
		for end < int64(len(image)) && image[end] != 0 && end-nameOffset < maxExportNameLen {
			end++
		}
		if end >= int64(len(image)) || image[end] != 0 {
			continue
		}
		if string(image[nameOffset:end]) != name {
			continue
		}
		ordinal := binary.LittleEndian.Uint16(image[ordinalAt : ordinalAt+2])
		functionAt := functionsOffset + int64(ordinal)*4
		if functionAt+4 > int64(len(image)) {
			return 0, &ErrNoExports{Reason: "the export address table is truncated"}
		}
		functionRVA := binary.LittleEndian.Uint32(image[functionAt : functionAt+4])
		// A forwarder's "RVA" lands inside the export directory itself; calling that as code reads a string as instructions.
		if functionRVA >= exportRVA && functionRVA < exportRVA+exportSize {
			return 0, &ErrForwardedExport{Name: name, RVA: functionRVA}
		}
		return functionRVA, nil
	}
	return 0, &ErrExportNotFound{Name: name, Available: int(numberOfNames)}
}

// fileOffsetForRVA maps an RVA to a file offset using the section table.
//
// Returning an error rather than the RVA itself is the point: an unmapped RVA means the image is not what the caller assumed, and
// treating "RVA as offset" is how a parser reads a section header as executable code.
func fileOffsetForRVA(image []byte, optionalOffset int64, sections int, rva uint32) (int64, error) {
	// SizeOfOptionalHeader sits four bytes before the optional header (machine, sections, timestamp, symtab, symbols, then size).
	sectionTable := optionalOffset + int64(binary.LittleEndian.Uint16(image[optionalOffset-4:optionalOffset-2]))
	for index := 0; index < sections; index++ {
		header := sectionTable + int64(index)*sectionHeaderSize
		if header+sectionHeaderSize > int64(len(image)) {
			return 0, fmt.Errorf("section table is truncated at entry %d", index)
		}
		virtualSize := binary.LittleEndian.Uint32(image[header+8 : header+12])
		virtualAddress := binary.LittleEndian.Uint32(image[header+12 : header+16])
		rawSize := binary.LittleEndian.Uint32(image[header+16 : header+20])
		rawPointer := binary.LittleEndian.Uint32(image[header+20 : header+24])
		// The section is addressable up to the larger of the two sizes; the raw data stops at rawSize.
		span := virtualSize
		if rawSize > span {
			span = rawSize
		}
		if rva >= virtualAddress && rva < virtualAddress+span {
			delta := rva - virtualAddress
			if delta >= rawSize {
				return 0, fmt.Errorf("RVA 0x%X lies in the uninitialised tail of a section", rva)
			}
			offset := int64(rawPointer) + int64(delta)
			if offset >= int64(len(image)) {
				return 0, fmt.Errorf("RVA 0x%X maps past the end of the image", rva)
			}
			return offset, nil
		}
	}
	return 0, fmt.Errorf("RVA 0x%X is not inside any section", rva)
}

// exportNameAt is a test helper kept next to the parser: it walks the name table and returns the first match as a forwarded string,
// so a caller can tell a forwarder apart from a body without re-deriving the offsets.
func exportNameAt(image []byte, offset int64) string {
	if offset < 0 || offset >= int64(len(image)) {
		return ""
	}
	end := bytes.IndexByte(image[offset:], 0)
	if end <= 0 {
		return ""
	}
	return string(image[offset : offset+int64(end)])
}

// FileOffsetForRVA maps an RVA to a file offset for a whole image.
//
// Exported because the two parsers speak different units and that difference is invisible at the call site: FindExportRVA returns an
// RVA, ExtractSSN reads bytes at a FILE OFFSET, and passing one where the other is expected produces a stub scan that starts in the
// wrong place - which reads as "no stub found" rather than as a unit error.
func FileOffsetForRVA(image []byte, rva uint32) (int64, error) {
	if len(image) < dosLfanewOffset+4 {
		return 0, &ErrNotPE{Reason: "image is too short to hold a DOS header"}
	}
	lfanew := int64(binary.LittleEndian.Uint32(image[dosLfanewOffset : dosLfanewOffset+4]))
	if lfanew <= 0 || lfanew+24 > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "e_lfanew points outside the image"}
	}
	coff := lfanew + 4
	sections := int(binary.LittleEndian.Uint16(image[coff+2 : coff+4]))
	optionalSize := int(binary.LittleEndian.Uint16(image[coff+16 : coff+18]))
	optional := coff + 20
	if optionalSize < 2 || optional+int64(optionalSize) > int64(len(image)) {
		return 0, &ErrNotPE{Reason: "optional header is missing or unreadable"}
	}
	return fileOffsetForRVA(image, optional, sections, rva)
}
