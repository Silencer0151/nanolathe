package main

import (
	"errors"

	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// disableUnavailableFrontendEntries is user-authorized host presentation
// policy in DESIGN_INTERFACE_HUD_INPUT §2.6. It runs on a fresh window, so
// mounted content can restore an action without clearing authored grey bits.
func (g *gameShell) disableUnavailableFrontendEntries(window *gui.Window, mode shellMode) {
	if g == nil || g.cs == nil || g.cs.fs == nil || g.assets == nil {
		return
	}
	disable := func(name string, unavailable bool) {
		if i := window.GadgetIndex(name); i >= 0 && unavailable {
			window.Gadgets[i].GrayedOut |= 1
		}
	}
	switch mode {
	case modeMenuMain:
		disable("SINGLE", !g.frontendPanelAvailable(modeMenuSingle))
		// Online rooms are skirmish or Survival battles, so MULTI needs what
		// those entries need: the browser's demo, with no skirmish maps,
		// greys it (DESIGN_MULTIPLAYER §16.6.2, DESIGN_BROWSER_HOST §3).
		disable("MULTI", !g.skirmishContentAvailable())
		disable("INTRO", !g.frontendFilesPresent(introPath))
		disable("Credits", !g.frontendFilesPresent(creditsMoviePath))
	case modeMenuSingle:
		maps := g.skirmishContentAvailable()
		disable("Skirmish", !maps)
		disable(survivalButton, !maps)
		campaign := g.frontendPanelAvailable(modeMenuMission) && g.frontendFilesPresent("guis/msnbrief.gui") && g.hasFrontendCampaign()
		disable("NewCamp", !campaign)
		disable("AnyMsn", !campaign || g.missionMenuLayout() != missionLayoutPlayAny)
		disable("Options", !g.frontendFilesPresent(retailOptionsGUI, retailOptionsBackdrop))
		disable("LoadGame", !g.frontendLoadDialogAvailable())
	case modeMenuMap:
		disable("GENMAP", !g.mapGeneratorAvailable())
	}
}

// skirmishContentAvailable reports that the mounted content can set up and
// play a skirmish: its setup and map screens load and it has a map.
func (g *gameShell) skirmishContentAvailable() bool {
	return g.frontendPanelAvailable(modeMenuSkirmish) && g.frontendPanelAvailable(modeMenuMap) && g.hasSkirmishTerrain()
}

func (g *gameShell) frontendPanelAvailable(mode shellMode) bool {
	panel := g.assets.panel[mode]
	return panel != nil && panel.window != nil && panel.unavailable == nil
}

// Census admission already requires a Network schema. Require a paired
// terrain file without decoding every map before the player opens a chooser.
func (g *gameShell) hasSkirmishTerrain() bool {
	for _, name := range g.maps {
		if g.frontendFilesPresent("maps/" + name + ".tnt") {
			return true
		}
	}
	return false
}

func (g *gameShell) hasFrontendCampaign() bool {
	if g.campaigns == nil {
		campaigns, err := mission.Discover(g.cs.fs)
		if err != nil {
			return false
		}
		g.campaigns = campaigns
	}
	for side := 0; side < 2; side++ {
		for _, campaign := range g.missionCampaignOptions(side) {
			if len(campaign.Missions) != 0 {
				return true
			}
		}
	}
	return false
}

func (g *gameShell) frontendLoadDialogAvailable() bool {
	if _, err := g.cs.fs.Stat(retailSaveLoadGUI); errors.Is(err, vfs.ErrNotFound) {
		return g.frontendFilesPresent("guis/loadlist.gui")
	}
	return g.frontendFilesPresent(retailSaveLoadGUI, loadScreenMode.backdrop())
}

// Metadata checks suppress known absent resources. Present but malformed
// files retain the existing decoder diagnostics at the action's load boundary.
func (g *gameShell) frontendFilesPresent(paths ...string) bool {
	for _, path := range paths {
		info, err := g.cs.fs.Stat(path)
		if errors.Is(err, vfs.ErrNotFound) || (err == nil && info.IsDir) {
			return false
		}
	}
	return true
}

// The options family's existing page table owns each GUI/backdrop pair.
// Missing templates disable their category rather than entering an empty page.
func (g *gameShell) disableUnavailableOptionsPages(window *gui.Window, inBattle bool) {
	for i, gadget := range window.Gadgets {
		key, isPage := retailOptionsPageKey(gui.CallbackName(gadget.Name))
		if !isPage || retailOptionsPageGadget(gadget) {
			continue
		}
		source := retailOptionsPages[key]
		available := g.frontendFilesPresent(source.source(inBattle))
		if !inBattle {
			available = available && g.frontendFilesPresent(source.backdrop)
		}
		if !available {
			window.Gadgets[i].GrayedOut |= 1
		}
	}
}
