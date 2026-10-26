package architecture

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	nanolatheModule   = "github.com/nanolathe-gg/nanolathe"
	modFetchPackage   = nanolatheModule + "/internal/modfetch"
	mapLibraryPackage = nanolatheModule + "/internal/maplibrary"
	addonLibrary      = nanolatheModule + "/internal/addonlibrary"
	modLibraryPackage = nanolatheModule + "/internal/modlibrary"
	desktopCommand    = nanolatheModule + "/cmd/nanolathe"
	relayCommand      = nanolatheModule + "/cmd/nanolathe-server"
	relayPackage      = nanolatheModule + "/internal/relay"
	lockstepPackage   = nanolatheModule + "/internal/lockstep"
	launcherPackage   = nanolatheModule + "/internal/launcher"
	launcherCommand   = nanolatheModule + "/cmd/nanolathe-launcher"
)

// isLauncherPackage reports whether an import path is the launcher's
// installation package or one beneath it.
func isLauncherPackage(importPath string) bool {
	return importPath == launcherPackage || strings.HasPrefix(importPath, launcherPackage+"/")
}

// TestNetworkStaysInTheModFetcher pins the mod library's network boundary
// (docs/DESIGN_MODS_MUTATORS.md §9 "Guards"):
//
//  1. internal/modfetch and the WebSocket relay are the only production
//     packages importing net/http. This is a DIRECT-import check: Ebitengine's ebitenutil
//     already pulls net/http into the dependency closure of the platform
//     adapter and the desktop command, so a closure check could not hold.
//  2. Only cmd/nanolathe may depend on internal/modfetch, so the displayless
//     command and every internal package stay off the network (D5).
//  3. No authoritative package depends on internal/modlibrary,
//     internal/maplibrary, internal/addonlibrary or internal/modfetch: content
//     selection happens before a session exists and never reaches a tick.
//  4. The launcher's release download (internal/launcher) is the third
//     package importing net/http. It belongs to a separate program: only
//     cmd/nanolathe-launcher depends on it, so neither the game nor the
//     displayless command can reach the network through it.
//
// Like the platform guard, it runs `go list -test` over the whole module.
// Desktop download integration tests may serve loopback fixtures through
// net/http and httptest. The ordinary desktop package is checked separately,
// so this exception cannot admit a production HTTP import.
func TestNetworkStaysInTheModFetcher(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("go", "list", "-test", "-f", "{{.ImportPath}}|{{join .Imports \" \"}}|{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -test ./...: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			continue
		}
		name, imports, deps := fields[0], strings.Fields(fields[1]), strings.Fields(fields[2])
		owner := owningPackage(name)

		// Multiplayer transport stays outside authoritative packages. The
		// relay handles opaque bytes and imports only the protocol leaf
		// (DESIGN_MULTIPLAYER §14, §16.4.2).
		for _, imported := range imports {
			if (imported == "net" || imported == "crypto/tls") && owner != modFetchPackage && owner != desktopCommand && owner != relayCommand && owner != relayPackage && owner != lockstepPackage {
				t.Errorf("%s imports transport %s outside the host/relay boundary", name, imported)
			}
			if owner == relayPackage && strings.HasPrefix(imported, nanolatheModule+"/") && imported != nanolatheModule+"/internal/netproto" && imported != relayPackage {
				t.Errorf("relay %s imports non-protocol package %s", name, imported)
			}
		}

		if owner != launcherCommand && !isLauncherPackage(owner) {
			for _, dep := range deps {
				if isLauncherPackage(dep) {
					t.Errorf("%s depends on %s; only %s may", name, dep, launcherCommand)
					break
				}
			}
		}

		if owner != modFetchPackage && owner != relayPackage && owner != launcherPackage {
			for _, imported := range imports {
				if owner == desktopCommand && name != desktopCommand && (imported == "net/http" || imported == "net/http/httptest") {
					continue
				}
				if imported == "net/http" || strings.HasPrefix(imported, "net/http/") {
					t.Errorf("%s imports %s; only %s and relay may", name, imported, modFetchPackage)
					break
				}
			}
		}
		if owner != modFetchPackage && owner != desktopCommand {
			for _, dep := range deps {
				if dep == modFetchPackage {
					t.Errorf("%s depends on %s; only %s may", name, modFetchPackage, desktopCommand)
					break
				}
			}
		}
		if isAuthoritativePackage(owner) {
			for _, dep := range deps {
				if dep == modFetchPackage || dep == modLibraryPackage || dep == mapLibraryPackage || dep == addonLibrary || dep == relayPackage || dep == lockstepPackage {
					t.Errorf("authoritative package %s depends on %s", name, dep)
					break
				}
			}
		}
	}
}

// isAuthoritativePackage reports whether an import path lies inside one of
// authoritativeDirs, the simulation side of the architecture boundary.
func isAuthoritativePackage(importPath string) bool {
	for _, dir := range authoritativeDirs {
		prefix := nanolatheModule + "/" + dir
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	return false
}
