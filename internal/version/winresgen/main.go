// Command winresgen writes a Windows resource object (.syso) that gives a
// release executable its version resource and icon. Code signing requires
// every signed executable to name the product and its version in a version
// resource, and Go writes none, so the native release build generates one
// for each Windows target after stamping the source (stampgen):
//
//	go run ./internal/version/winresgen -arch amd64 -version 0.3.0.412 \
//	    -name nanolathe.exe -description Nanolathe \
//	    -icon tools/installer/windows/Nanolathe.ico \
//	    -out cmd/nanolathe/rsrc_windows_amd64.syso
//
// The object is not part of the stamped source inventory, so it leaves the
// build manifest unchanged; its file name suffix links it into that one
// GOOS/GOARCH only. It carries no application manifest, so the executable's
// DPI and compatibility behavior stay as they are without one.
//
// The object is a COFF file with one .rsrc section: the resource directory
// tree, its data entries and the resource bytes. Each data entry's address
// is relocated, because only the linker knows where the section lands.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// productName is the name signing policy requires every release executable
// to carry; it is not configurable.
const productName = "Nanolathe"

// copyright is the LICENSE notice.
const copyright = "Copyright (c) 2026 Daniel G. Taylor and Nanolathe contributors"

// languageUSEnglish and codePageUnicode label the one string table: U.S.
// English text in UTF-16, the table Windows' file properties read first.
const (
	languageUSEnglish = 0x0409
	codePageUnicode   = 0x04b0
)

// Resource types (winuser.h).
const (
	typeIcon      = 3
	typeGroupIcon = 14
	typeVersion   = 16
)

// machines are the COFF machine types and the relocation type that adds a
// section's image-relative address for each supported architecture.
var machines = map[string]struct{ machine, addr32NB uint16 }{
	"amd64": {0x8664, 0x0003}, // IMAGE_REL_AMD64_ADDR32NB
	"arm64": {0xaa64, 0x0002}, // IMAGE_REL_ARM64_ADDR32NB
}

// genError is the generator's one diagnostic shape.
func genError(what, logicalPath, expected string) error {
	return fmt.Errorf("nanolathe: %s: logical path %s, providers searched [resource inputs], expected %s", what, logicalPath, expected)
}

func run(args []string) error {
	flags := flag.NewFlagSet("winresgen", flag.ContinueOnError)
	arch := flags.String("arch", "", "the target GOARCH: amd64 or arm64")
	ver := flags.String("version", "", "the file and product version, four dot-separated numbers 0-65535")
	name := flags.String("name", "", "the executable's file name, such as nanolathe.exe")
	description := flags.String("description", "", "the file description Windows shows")
	icon := flags.String("icon", "", "an .ico file to embed as the executable's icon (optional)")
	out := flags.String("out", "", "the .syso file to write")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return genError("unexpected argument "+strconv.Quote(flags.Arg(0)), "<command line>", "flags only")
	}
	m, ok := machines[*arch]
	if !ok {
		return genError("unsupported architecture "+strconv.Quote(*arch), "<command line>", "-arch amd64 or arm64")
	}
	if *name == "" || *description == "" || *out == "" {
		return genError("missing argument", "<command line>", "-name, -description and -out")
	}
	numbers, err := parseVersion(*ver)
	if err != nil {
		return err
	}
	var resources []resource
	if *icon != "" {
		data, err := os.ReadFile(*icon)
		if err != nil {
			return genError("icon unreadable", *icon, "a readable .ico file")
		}
		if resources, err = iconResources(data, *icon); err != nil {
			return err
		}
	}
	resources = append(resources, resource{typ: typeVersion, id: 1, data: versionInfo(numbers, *ver, *name, *description)})
	return os.WriteFile(*out, object(m.machine, m.addr32NB, resources), 0o644)
}

// parseVersion reads the four 16-bit fields of a Windows file version.
func parseVersion(s string) ([4]uint16, error) {
	var v [4]uint16
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return v, genError("version "+strconv.Quote(s)+" refused", "<command line>", "four dot-separated numbers 0-65535")
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || strconv.FormatUint(n, 10) != p {
			return v, genError("version "+strconv.Quote(s)+" refused", "<command line>", "four dot-separated numbers 0-65535")
		}
		v[i] = uint16(n)
	}
	return v, nil
}

// resource is one entry of the resource tree, always in languageUSEnglish.
type resource struct {
	typ, id uint16
	data    []byte
}

// iconResources splits an .ico file into one icon resource per image and
// the group resource that lists them, which Windows uses as the
// executable's icon. A group entry has the file's directory fields, with
// the image's resource ID in place of its file offset.
func iconResources(ico []byte, path string) ([]resource, error) {
	bad := genError("icon malformed", path, "an .ico file with at least one image")
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:]) != 0 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, bad
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	if count == 0 || len(ico) < 6+16*count {
		return nil, bad
	}
	group := append([]byte(nil), ico[:6]...)
	var images []resource
	for i := range count {
		entry := ico[6+16*i : 6+16*(i+1)]
		size := binary.LittleEndian.Uint32(entry[8:])
		offset := binary.LittleEndian.Uint32(entry[12:])
		if size == 0 || uint64(offset)+uint64(size) > uint64(len(ico)) {
			return nil, bad
		}
		id := uint16(i + 1)
		images = append(images, resource{typ: typeIcon, id: id, data: ico[offset : offset+size]})
		group = append(group, entry[:12]...)
		group = binary.LittleEndian.AppendUint16(group, id)
	}
	return append(images, resource{typ: typeGroupIcon, id: 1, data: group}), nil
}

// versionInfo is the VS_VERSIONINFO resource: the fixed binary version
// fields, then one U.S. English Unicode string table and the translation
// that points file properties at it.
func versionInfo(v [4]uint16, text, name, description string) []byte {
	ms := uint32(v[0])<<16 | uint32(v[1])
	ls := uint32(v[2])<<16 | uint32(v[3])
	var fixed []byte
	for _, field := range []uint32{
		0xfeef04bd, // signature
		0x00010000, // structure version 1.0
		ms, ls,     // file version
		ms, ls, // product version
		0x3f,    // every defined flag bit is meaningful
		0,       // no flags: not debug, prerelease, patched or private
		0x40004, // VOS_NT_WINDOWS32
		1,       // VFT_APP
		0, 0, 0, // no subtype or file date
	} {
		fixed = binary.LittleEndian.AppendUint32(fixed, field)
	}
	fields := [][2]string{
		{"FileDescription", description},
		{"FileVersion", text},
		{"InternalName", strings.TrimSuffix(name, ".exe")},
		{"LegalCopyright", copyright},
		{"OriginalFilename", name},
		{"ProductName", productName},
		{"ProductVersion", text},
	}
	var table [][]byte
	for _, s := range fields {
		value := utf16z(s[1])
		table = append(table, versionBlock(s[0], value, uint16(len(value)/2), true))
	}
	translation := binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, languageUSEnglish), codePageUnicode)
	return versionBlock("VS_VERSION_INFO", fixed, uint16(len(fixed)), false,
		versionBlock("StringFileInfo", nil, 0, true,
			versionBlock(fmt.Sprintf("%04X%04X", languageUSEnglish, codePageUnicode), nil, 0, true, table...)),
		versionBlock("VarFileInfo", nil, 0, true,
			versionBlock("Translation", translation, uint16(len(translation)), false)))
}

// versionBlock is one node of a version resource: its length, value length
// and type, its NUL-terminated UTF-16 key, its value and its children, each
// starting on a 32-bit boundary. Its length leaves out trailing padding.
// valueLength counts characters for text values and bytes otherwise.
func versionBlock(key string, value []byte, valueLength uint16, text bool, children ...[]byte) []byte {
	b := make([]byte, 6, 64)
	b = append(b, utf16z(key)...)
	b = align(b, 4)
	b = append(b, value...)
	for _, child := range children {
		b = align(b, 4)
		b = append(b, child...)
	}
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[2:], valueLength)
	if text {
		binary.LittleEndian.PutUint16(b[4:], 1)
	}
	return b
}

func utf16z(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}

func align(b []byte, n int) []byte {
	for len(b)%n != 0 {
		b = append(b, 0)
	}
	return b
}

// object lays the resources out as a COFF object: the file header, one
// .rsrc section header, the section bytes, one relocation per data entry,
// and a symbol table holding the section's own symbol, which the
// relocations name.
//
// The section is the resource directory tree (type, then ID, then
// language), every directory before the data entries and the data last.
// Directory entries are ascending by ID, as Windows searches them.
func object(machine, addr32NB uint16, resources []resource) []byte {
	slices.SortFunc(resources, func(a, b resource) int {
		if a.typ != b.typ {
			return int(a.typ) - int(b.typ)
		}
		return int(a.id) - int(b.id)
	})
	var types []uint16
	for _, r := range resources {
		if len(types) == 0 || types[len(types)-1] != r.typ {
			types = append(types, r.typ)
		}
	}
	const directory, entry, dataEntry = 16, 8, 16
	offset := directory + entry*len(types)
	typeOffsets := map[uint16]int{}
	for _, t := range types {
		typeOffsets[t] = offset
		offset += directory + entry*countType(resources, t)
	}
	languageOffsets := make([]int, len(resources))
	for i := range resources {
		languageOffsets[i] = offset
		offset += directory + entry
	}
	dataEntryOffsets := make([]int, len(resources))
	for i := range resources {
		dataEntryOffsets[i] = offset
		offset += dataEntry
	}
	dataOffsets := make([]int, len(resources))
	for i, r := range resources {
		offset = (offset + 7) &^ 7
		dataOffsets[i] = offset
		offset += len(r.data)
	}

	section := make([]byte, offset)
	le := binary.LittleEndian
	writeDirectory := func(at, ids int) {
		le.PutUint16(section[at+14:], uint16(ids)) // ID entries; there are no named ones
	}
	const subdirectory = 0x80000000
	writeDirectory(0, len(types))
	for i, t := range types {
		le.PutUint32(section[directory+entry*i:], uint32(t))
		le.PutUint32(section[directory+entry*i+4:], subdirectory|uint32(typeOffsets[t]))
	}
	next := map[uint16]int{}
	for i, r := range resources {
		at := typeOffsets[r.typ]
		if next[r.typ] == 0 {
			writeDirectory(at, countType(resources, r.typ))
		}
		slot := at + directory + entry*next[r.typ]
		next[r.typ]++
		le.PutUint32(section[slot:], uint32(r.id))
		le.PutUint32(section[slot+4:], subdirectory|uint32(languageOffsets[i]))
		writeDirectory(languageOffsets[i], 1)
		le.PutUint32(section[languageOffsets[i]+directory:], languageUSEnglish)
		le.PutUint32(section[languageOffsets[i]+directory+4:], uint32(dataEntryOffsets[i]))
		// The address is the data's section offset until relocation adds
		// the section's image-relative address.
		le.PutUint32(section[dataEntryOffsets[i]:], uint32(dataOffsets[i]))
		le.PutUint32(section[dataEntryOffsets[i]+4:], uint32(len(r.data)))
		copy(section[dataOffsets[i]:], r.data)
	}

	const fileHeader, sectionHeader, relocation = 20, 40, 10
	sectionStart := fileHeader + sectionHeader
	relocationStart := sectionStart + len(section)
	symbolStart := relocationStart + relocation*len(resources)
	var b bytes.Buffer
	write := func(v any) { _ = binary.Write(&b, le, v) }
	write(machine)
	write(uint16(1))                   // sections
	write(uint32(0))                   // timestamp: reproducible output
	write(uint32(symbolStart))         // symbol table
	write(uint32(1))                   // symbols
	write(uint16(0))                   // no optional header in an object
	write(uint16(0))                   // characteristics
	b.WriteString(".rsrc\x00\x00\x00") // section name
	write(uint32(0))                   // virtual size
	write(uint32(0))                   // virtual address
	write(uint32(len(section)))
	write(uint32(sectionStart))
	write(uint32(relocationStart))
	write(uint32(0)) // no line numbers
	write(uint16(len(resources)))
	write(uint16(0))
	write(uint32(0x40000040)) // initialized, readable data
	b.Write(section)
	for _, at := range dataEntryOffsets {
		write(uint32(at)) // the data entry's address field
		write(uint32(0))  // symbol 0, the section
		write(addr32NB)
	}
	b.WriteString(".rsrc\x00\x00\x00") // symbol name
	write(uint32(0))                   // value: the section's start
	write(uint16(1))                   // section number
	write(uint16(0))                   // type
	write(uint8(3))                    // IMAGE_SYM_CLASS_STATIC
	write(uint8(0))                    // no auxiliary records
	write(uint32(4))                   // empty string table: its own length
	return b.Bytes()
}

func countType(resources []resource, typ uint16) int {
	n := 0
	for _, r := range resources {
		if r.typ == typ {
			n++
		}
	}
	return n
}
