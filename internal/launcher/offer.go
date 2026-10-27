package launcher

import (
	"context"
	"errors"
	"fmt"
)

// Standing is where a channel's release stands against the installed
// build. The launcher only ever moves a player forward, ordering builds by
// source revision ancestry: GitHub's compare of the installed revision
// with the release's, on the release repository.
type Standing int

const (
	// Forward: the release is ahead of the installed build, or no build
	// with a known revision is installed.
	Forward Standing = iota
	// Current: the release is the installed build, or built from the same
	// revision.
	Current
	// Backward: the installed build is ahead of the release, or carries
	// history the release lacks, so it is kept. A Stable player whose
	// Latest build is newer stays on it until Stable passes it.
	Backward
)

// ErrBackward refuses an update that would move the installed build back.
var ErrBackward = errors.New("nanolathe: the installed build is newer than the release")

// Offer is a channel check's answer: the channel's release and where it
// stands against the installed build.
type Offer struct {
	Release  Release
	Standing Standing
}

// Offer finds a channel's release for goos/goarch and places it against
// the installed build. A comparison that fails is an error, never an
// update.
func (in Install) Offer(ctx context.Context, src Source, ch Channel, goos, goarch string) (Offer, error) {
	rel, err := src.Find(ctx, ch, goos, goarch)
	if err != nil {
		return Offer{}, err
	}
	st, err := in.standing(ctx, src, rel, nil)
	if err != nil {
		return Offer{}, err
	}
	return Offer{Release: rel, Standing: st}, nil
}

// standing places rel against the installed build. A release with another
// tag is compared by the revision its verified build.json records; offered
// is that record when the caller already holds it.
func (in Install) standing(ctx context.Context, src Source, rel Release, offered *BuildInfo) (Standing, error) {
	sel, ok := in.Selected()
	switch {
	case ok && sel.Current(rel):
		return Current, nil
	case !ok || sel.Revision == "":
		return Forward, nil
	}
	if offered == nil {
		rec, err := src.buildRecord(ctx, rel)
		if err != nil {
			return 0, err
		}
		offered = &rec.info
	}
	return src.compare(ctx, sel.Revision, offered.Revision)
}

// compare asks the release repository how head stands against base. The
// second page of one-commit pages still carries GitHub's status, without
// the changed-file list the first page adds, which can run to megabytes.
func (s Source) compare(ctx context.Context, base, head string) (Standing, error) {
	var r struct {
		Status string `json:"status"`
	}
	if err := s.getJSON(ctx, s.API+"/compare/"+base+"..."+head+"?per_page=1&page=2", &r); err != nil {
		return 0, fmt.Errorf("nanolathe: could not compare the installed build %.12s with the release's %.12s: %w", base, head, err)
	}
	switch r.Status {
	case "ahead":
		return Forward, nil
	case "identical":
		return Current, nil
	case "behind", "diverged":
		return Backward, nil
	}
	return 0, fmt.Errorf("nanolathe: GitHub compared %.12s with %.12s as %q", base, head, r.Status)
}
