//go:build !js

package film

import "embed"

// These are presentation assets, independent of the retail game's fonts.
// Provenance, reproduction instructions and OFL terms accompany the atlases.
// The executable keeps both the base pair and the Chinese additions: the latter
// cost nothing until a language asks for them, which is what keeps nanolathe.exe
// a single file.
//
//go:embed assets/*.png assets/*.json assets/OFL.txt assets/README.md
var fontAssets embed.FS

func init() {
	// An embedded build always has the Chinese atlases at hand, so asking for
	// them is all it takes.
	startCJKFetch = func() { cjkReady.Store(true) }
}

func readAsset(name string) ([]byte, error) {
	return fontAssets.ReadFile("assets/" + name)
}
