// Package windowsicon embeds the Nanolathe mark's Windows icon (see
// README.md), so programs can show it without a separate copy.
package windowsicon

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/png"
)

// ICO is Nanolathe.ico.
//
//go:embed Nanolathe.ico
var ICO []byte

// Images decodes the icon's PNG images, smallest first, for a window icon.
func Images() []image.Image {
	if len(ICO) < 6 {
		return nil
	}
	var images []image.Image
	count := int(binary.LittleEndian.Uint16(ICO[4:]))
	for i := range count {
		entry := ICO[6+16*i:]
		size := binary.LittleEndian.Uint32(entry[8:])
		offset := binary.LittleEndian.Uint32(entry[12:])
		if uint64(offset)+uint64(size) > uint64(len(ICO)) {
			continue
		}
		if img, err := png.Decode(bytes.NewReader(ICO[offset : offset+size])); err == nil {
			images = append(images, img)
		}
	}
	return images
}
