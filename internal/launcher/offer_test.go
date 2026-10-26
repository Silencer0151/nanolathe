package launcher

import (
	"context"
	"errors"
	"testing"
)

// TestOfferFallsBackToLatestOnlyForAnUnchosenChannel: before the first
// Stable release, a player who never chose a channel is offered Latest; a
// player who chose Stable is told it has nothing yet; once Stable has a
// release, an unchosen player gets it.
func TestOfferFallsBackToLatestOnlyForAnUnchosenChannel(t *testing.T) {
	repo := newFakeRepo(t)
	repo.releases = []apiRelease{{Tag: "v0.3.0"}}
	repo.publish("native-7", true, "0.3.0.7", game(), false)
	src, ctx := repo.source(), context.Background()

	rel, ch, err := Offer(ctx, src, Settings{Channel: Stable}, false, "windows", "amd64")
	if err != nil || ch != Latest || rel.Tag != "native-7" {
		t.Fatalf("unchosen, no Stable: %s from %s, %v; want native-7 from latest", rel.Tag, ch, err)
	}
	if _, ch, err := Offer(ctx, src, Settings{Channel: Stable}, true, "windows", "amd64"); !errors.Is(err, ErrNoRelease) || ch != Stable {
		t.Fatalf("chose Stable: %s, %v; want ErrNoRelease from stable", ch, err)
	}
	repo.publish("v0.4.0", false, "0.4.0.0", game(), false)
	if rel, ch, err := Offer(ctx, src, Settings{Channel: Latest}, false, "windows", "amd64"); err != nil || ch != Stable || rel.Tag != "v0.4.0" {
		t.Fatalf("unchosen with Stable: %s from %s, %v; want v0.4.0 from stable", rel.Tag, ch, err)
	}
	if rel, ch, err := Offer(ctx, src, Settings{Channel: Latest}, true, "windows", "amd64"); err != nil || ch != Latest || rel.Tag != "native-7" {
		t.Fatalf("chose Latest: %s from %s, %v; want native-7 from latest", rel.Tag, ch, err)
	}
}
