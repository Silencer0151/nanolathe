package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// readTree walks a .rsrc section the way the loader does, returning each
// leaf's bytes by type and ID. Data entry addresses hold section offsets
// here: relocation would add the section's address.
func readTree(t *testing.T, section []byte) map[[2]uint32][]byte {
	t.Helper()
	le := binary.LittleEndian
	entries := func(at uint32) [][2]uint32 {
		n := uint32(le.Uint16(section[at+12:])) + uint32(le.Uint16(section[at+14:]))
		var out [][2]uint32
		for i := range n {
			e := at + 16 + 8*i
			out = append(out, [2]uint32{le.Uint32(section[e:]), le.Uint32(section[e+4:])})
		}
		return out
	}
	leaves := map[[2]uint32][]byte{}
	for _, typ := range entries(0) {
		for _, id := range entries(typ[1] &^ 0x80000000) {
			languages := entries(id[1] &^ 0x80000000)
			if len(languages) != 1 || languages[0][0] != languageUSEnglish || languages[0][1]&0x80000000 != 0 {
				t.Fatalf("resource %d/%d languages %v, want one U.S. English leaf", typ[0], id[0], languages)
			}
			data := languages[0][1]
			at, size := le.Uint32(section[data:]), le.Uint32(section[data+4:])
			leaves[[2]uint32{typ[0], id[0]}] = section[at : at+size]
		}
	}
	return leaves
}

// versionStrings decodes the string table of a VS_VERSIONINFO resource.
func versionStrings(t *testing.T, info []byte) map[string]string {
	t.Helper()
	le := binary.LittleEndian
	type node struct {
		key      string
		value    []byte
		children []byte
	}
	parse := func(b []byte) node {
		length, valueLength, text := int(le.Uint16(b)), int(le.Uint16(b[2:])), le.Uint16(b[4:]) == 1
		b = b[:length]
		i := 6
		var key []uint16
		for ; le.Uint16(b[i:]) != 0; i += 2 {
			key = append(key, le.Uint16(b[i:]))
		}
		i = (i + 2 + 3) &^ 3
		if text {
			valueLength *= 2
		}
		n := node{key: string(utf16.Decode(key)), value: b[i : i+valueLength]}
		n.children = b[min((i+valueLength+3)&^3, length):]
		return n
	}
	children := func(b []byte) []node {
		var out []node
		for len(b) >= 6 {
			n := parse(b)
			out = append(out, n)
			b = b[min((int(le.Uint16(b))+3)&^3, len(b)):]
		}
		return out
	}
	root := parse(info)
	if root.key != "VS_VERSION_INFO" || le.Uint32(root.value) != 0xfeef04bd {
		t.Fatalf("root %q, signature %x", root.key, root.value[:4])
	}
	out := map[string]string{}
	for _, block := range children(root.children) {
		if block.key != "StringFileInfo" {
			continue
		}
		for _, table := range children(block.children) {
			if table.key != "040904B0" {
				t.Fatalf("string table %q, want 040904B0", table.key)
			}
			for _, s := range children(table.children) {
				var units []uint16
				for i := 0; i+1 < len(s.value); i += 2 {
					units = append(units, le.Uint16(s.value[i:]))
				}
				out[s.key] = string(utf16.Decode(units[:len(units)-1]))
			}
		}
	}
	return out
}

// TestObjectCarriesVersionAndIcon locks what signing and Windows file
// properties read: the product name, both version forms, and an icon group
// that names every image of the input.
func TestObjectCarriesVersionAndIcon(t *testing.T) {
	dir := t.TempDir()
	icon := filepath.Join("..", "..", "..", "tools", "installer", "windows", "Nanolathe.ico")
	ico, err := os.ReadFile(icon)
	if err != nil {
		t.Fatal(err)
	}
	for arch, want := range map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64} {
		out := filepath.Join(dir, arch+".syso")
		if err := run([]string{"-arch", arch, "-version", "0.3.0.412", "-name", "nanolathe.exe", "-description", "Nanolathe", "-icon", icon, "-out", out}); err != nil {
			t.Fatal(err)
		}
		f, err := pe.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		if f.Machine != want || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" {
			t.Fatalf("%s: machine %#x, sections %d", arch, f.Machine, len(f.Sections))
		}
		section, err := f.Sections[0].Data()
		if err != nil {
			t.Fatal(err)
		}
		leaves := readTree(t, section)
		images := int(binary.LittleEndian.Uint16(ico[4:]))
		if len(f.Sections[0].Relocs) != len(leaves) || len(leaves) != images+2 {
			t.Fatalf("%s: %d relocations, %d resources, want %d each", arch, len(f.Sections[0].Relocs), len(leaves), images+2)
		}
		group := leaves[[2]uint32{typeGroupIcon, 1}]
		if !bytes.Equal(group[:6], ico[:6]) || len(group) != 6+14*images {
			t.Fatalf("%s: icon group header %x, length %d", arch, group[:6], len(group))
		}
		got := versionStrings(t, leaves[[2]uint32{typeVersion, 1}])
		for key, value := range map[string]string{"ProductName": "Nanolathe", "ProductVersion": "0.3.0.412", "FileVersion": "0.3.0.412", "OriginalFilename": "nanolathe.exe"} {
			if got[key] != value {
				t.Errorf("%s: %s = %q, want %q", arch, key, got[key], value)
			}
		}
		f.Close()
	}
}

// TestVersionRefused: Windows keeps four 16-bit fields; anything else
// would be silently truncated.
func TestVersionRefused(t *testing.T) {
	for _, v := range []string{"", "0.3.0", "0.3.0.412.1", "0.3.0.65536", "0.3.0.-1", "0.3.0.0412", "v0.3.0.1", "0.3.0.x"} {
		if _, err := parseVersion(v); err == nil {
			t.Errorf("version %q accepted", v)
		}
	}
	if v, err := parseVersion("0.3.0.65535"); err != nil || v != [4]uint16{0, 3, 0, 65535} {
		t.Errorf("0.3.0.65535 = %v, %v", v, err)
	}
}
