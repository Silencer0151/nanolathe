// Package shell is the launcher's desktop integration: shortcuts, the
// Windows Apps & Features entry, the folder picker, opening folders and
// confirmations. Windows has all of it; elsewhere the source installer's
// script makes the shortcut, and the rest uses what the desktop offers.
// This is Nanolathe host installation policy, not retail behavior.
package shell

// Entries describe an installed launcher to the desktop.
type Entries struct {
	// Base is the installation directory; Launcher is the installed
	// launcher executable within it.
	Base, Launcher string
	// Version is shown in Apps & Features.
	Version string
	// Desktop also places a shortcut on the desktop.
	Desktop bool
}

// AppControl is the state of Windows Smart App Control, which blocks
// unsigned programs it does not recognize, with no option to run them.
type AppControl int

const (
	AppControlOff AppControl = iota
	AppControlOn
	// AppControlEvaluation is Windows deciding whether to turn it on.
	AppControlEvaluation
)
