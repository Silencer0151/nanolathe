package film

import (
	"errors"
	"testing"
)

// The base atlases carry Latin and Cyrillic only: a build that never selects
// Chinese must not hold a Han glyph, or every player pays for the atlas.
func TestBaseFaceHasNoHanGlyphs(t *testing.T) {
	face := filmFaces()["display"]
	if _, r := face.glyph('选'); r != ' ' {
		t.Fatalf("the base face resolves 选 to %q, want the space fallback", r)
	}
}

// EnableCJK merges the Chinese atlases into both faces and leaves the base
// entries alone, so Latin text keeps the glyphs and metrics it always had.
func TestCJKFaceAddsHanGlyphs(t *testing.T) {
	latin := textFace("display").Glyphs["A"]
	EnableCJK()
	merged := textFace("display")
	g, r := merged.glyph('选')
	if r != '选' {
		t.Fatalf("选 resolves to %q after EnableCJK", r)
	}
	if g.units() < 1.5 {
		t.Fatalf("选 units = %v, want the rasterized-below-atlas scale", g.units())
	}
	if len(g.levels) == 0 {
		t.Fatal("选 has no coverage mipmaps")
	}
	if merged.Glyphs["A"] != latin {
		t.Fatal("the merged face replaced a Latin glyph instead of keeping the base entry")
	}
}

// A repeated selection shares the in-flight request; failure makes the next
// explicit selection a retry instead of permanently hiding its glyphs.
func TestCJKRequestDeduplicatesAndRetries(t *testing.T) {
	enabled, ready, failure, start := cjkEnabled.Load(), cjkReady.Load(), cjkFailure.Load(), startCJKFetch
	t.Cleanup(func() {
		cjkEnabled.Store(enabled)
		cjkReady.Store(ready)
		cjkFailure.Store(failure)
		startCJKFetch = start
	})
	cjkEnabled.Store(false)
	cjkReady.Store(false)
	cjkFailure.Store(nil)
	calls := 0
	startCJKFetch = func() { calls++ }
	EnableCJK()
	EnableCJK()
	if calls != 1 || CJKReady() {
		t.Fatalf("in flight: requests=%d ready=%v", calls, CJKReady())
	}
	err := errors.New("download failed")
	cjkFailure.Store(&err)
	cjkEnabled.Store(false)
	if CJKError() == nil {
		t.Fatal("download failure was lost")
	}
	EnableCJK()
	if calls != 2 || CJKError() != nil {
		t.Fatalf("retry: requests=%d failure=%v", calls, CJKError())
	}
	cjkReady.Store(true)
	EnableCJK()
	if calls != 2 || !CJKReady() {
		t.Fatalf("complete: requests=%d ready=%v", calls, CJKReady())
	}
}
