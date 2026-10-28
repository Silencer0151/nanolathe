//go:build !retail

package replay_test

import (
	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// A takeover hands the recording's other humans to the Modern AI
// (DESIGN_MULTIPLAYER §10.1). Only a command links the real one, so the fast
// tier installs this stand-in, which decides as the retail step does; the
// retail tier links the real one (session_retail_test.go).
type stubModernAIStep struct{}

func (stubModernAIStep) Step(m *ai.Manager, tick uint32, w *units.World, econ *economy.Service) {
	ai.RetailPlanner{}.Step(m, tick, w, econ)
}

func (stubModernAIStep) ControlsModernAI(*ai.Manager) bool { return true }

func init() { session.RegisterModernAI(stubModernAIStep{}) }
