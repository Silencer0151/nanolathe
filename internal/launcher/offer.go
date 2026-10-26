package launcher

import (
	"context"
	"errors"
)

// Offer finds the release a launcher offers: the chosen channel's, or for a
// player who has not chosen one, Stable's, then Latest's while Stable has
// no release yet (before the first signed release). It returns the channel
// the release came from.
func Offer(ctx context.Context, src Source, s Settings, chosen bool, goos, goarch string) (Release, Channel, error) {
	ch := s.Channel
	if !chosen {
		ch = Stable
	}
	rel, err := src.Find(ctx, ch, goos, goarch)
	if errors.Is(err, ErrNoRelease) && !chosen {
		ch = Latest
		rel, err = src.Find(ctx, ch, goos, goarch)
	}
	return rel, ch, err
}
