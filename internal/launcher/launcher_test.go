package launcher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRepo serves an authored GitHub release listing and its assets.
type fakeRepo struct {
	t        *testing.T
	releases []apiRelease
	files    map[string][]byte // asset URL path → bytes
	server   *httptest.Server
}

func newFakeRepo(t *testing.T) *fakeRepo {
	r := &fakeRepo{t: t, files: map[string][]byte{}}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/repo/releases":
			_ = json.NewEncoder(w).Encode(r.releases)
		case "/repo/releases/latest":
			for _, rel := range r.releases {
				if !rel.Draft && !rel.Prerelease {
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
			}
			http.NotFound(w, req)
		default:
			if data, ok := r.files[req.URL.Path]; ok {
				_, _ = w.Write(data)
				return
			}
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *fakeRepo) source() Source {
	return Source{API: r.server.URL + "/repo", Client: r.server.Client()}
}

// publish adds a release whose package holds files, with build.json and a
// SHA256SUMS that lists every asset; corrupt changes the package bytes
// after its sum is taken.
func (r *fakeRepo) publish(tag string, prerelease bool, version string, files map[string]string, corrupt bool) {
	var pkg bytes.Buffer
	z := zip.NewWriter(&pkg)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			r.t.Fatal(err)
		}
		_, _ = w.Write([]byte(data))
	}
	if err := z.Close(); err != nil {
		r.t.Fatal(err)
	}
	info := fmt.Sprintf(`{"schema": 1, "revision": "%040d", "version": %q, "build_manifest": "x"}`, len(r.releases), version)
	assets := map[string][]byte{PackageAsset("windows", "amd64"): pkg.Bytes(), buildInfoAsset: []byte(info)}
	var sums strings.Builder
	for _, name := range []string{PackageAsset("windows", "amd64"), buildInfoAsset} {
		sum := sha256.Sum256(assets[name])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	assets[checksumsAsset] = []byte(sums.String())
	if corrupt {
		assets[PackageAsset("windows", "amd64")] = append(pkg.Bytes(), 0)
	}
	rel := apiRelease{Tag: tag, Prerelease: prerelease}
	for name, data := range assets {
		path := "/download/" + tag + "/" + name
		r.files[path] = data
		rel.Assets = append(rel.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{name, r.server.URL + path})
	}
	r.releases = append([]apiRelease{rel}, r.releases...)
}

func game(extra ...string) map[string]string {
	files := map[string]string{"nanolathe.exe": "game", "source-revision": "r\n", "LICENSE": "MIT"}
	for i := 0; i+1 < len(extra); i += 2 {
		files[extra[i]] = extra[i+1]
	}
	return files
}

func windowsInstall(t *testing.T) Install {
	if runtime.GOOS != "windows" {
		// The Windows layout is plain files, which every host can test; the
		// Unix layout's symbolic link is exercised on Unix hosts below.
		t.Log("testing the Windows layout on", runtime.GOOS)
	}
	return Install{Base: t.TempDir(), GOOS: "windows"}
}

func noStartCheck(t *testing.T) {
	saved := startCheck
	startCheck = func(context.Context, string) error { return nil }
	t.Cleanup(func() { startCheck = saved })
}

// TestFindOffersEachChannelsNewestCompleteRelease: Stable is the latest
// published vX.Y.Z (none until one carries native assets); Latest is the
// highest-numbered complete native-<run>, whatever the listing order.
func TestFindOffersEachChannelsNewestCompleteRelease(t *testing.T) {
	repo := newFakeRepo(t)
	repo.releases = []apiRelease{{Tag: "v0.3.0"}} // published before releases carried builds
	src, ctx := repo.source(), context.Background()
	if _, err := src.Find(ctx, Stable, "windows", "amd64"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("Stable without assets: %v, want ErrNoRelease", err)
	}
	repo.publish("native-12", true, "0.3.0.12", game(), false)
	repo.publish("native-9", true, "0.3.0.9", game(), false)
	repo.releases = append(repo.releases, apiRelease{Tag: "native-14", Prerelease: true}, apiRelease{Tag: "browser-20", Prerelease: true})
	rel, err := src.Find(ctx, Latest, "windows", "amd64")
	if err != nil || rel.Tag != "native-12" {
		t.Fatalf("Latest = %q, %v; want native-12 (native-14 lacks assets)", rel.Tag, err)
	}
	if _, err := src.Find(ctx, Latest, "linux", "arm64"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("Latest for a platform no release builds: %v, want ErrNoRelease", err)
	}
	repo.publish("v0.4.0", false, "0.4.0.0", game(), false)
	if rel, err := src.Find(ctx, Stable, "windows", "amd64"); err != nil || rel.Tag != "v0.4.0" {
		t.Fatalf("Stable = %q, %v; want v0.4.0", rel.Tag, err)
	}
}

// sourceInstall lays out what the PowerShell source installer leaves.
func sourceInstall(t *testing.T, in Install) {
	for path, data := range map[string]string{
		"releases/main-297b0734f02f-x-y/nanolathe.exe": "old", "releases/main-297b0734f02f-x-y/source-revision": strings.Repeat("a", 40),
		"releases/main-111111111111-x-y/nanolathe.exe": "older",
		"current.txt": "main-297b0734f02f-x-y", "root.txt": t.TempDir(), "settings.json": "{}", "saves/game.SAV": "save",
		"toolchains/go/bin/go.exe": "go", "cache/build/x": "cache", "work-abc/source.zip": "zip", "install.lock": "",
	} {
		path = filepath.Join(in.Base, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(in Install, path string) bool {
	_, err := os.Stat(in.Path(filepath.FromSlash(path)))
	return err == nil
}

// TestUpdateAdoptsASourceInstallation: the first update selects the
// downloaded build, keeps the player's files and the previous build, and
// removes the source installer's compiler and caches; the next update
// prunes the oldest build.
func TestUpdateAdoptsASourceInstallation(t *testing.T) {
	noStartCheck(t)
	repo := newFakeRepo(t)
	repo.publish("native-12", true, "0.3.0.12", game(), false)
	in := windowsInstall(t)
	sourceInstall(t, in)
	before, _ := in.Selected()
	if before.Tag != "" || before.Version != "main-aaaaaaaaaaaa" {
		t.Fatalf("source-built selection = %+v", before)
	}
	src, ctx := repo.source(), context.Background()
	rel, err := src.Find(ctx, Latest, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Update(ctx, src, rel, "windows", "amd64", nil); err != nil {
		t.Fatal(err)
	}
	sel, ok := in.Selected()
	if !ok || sel.Tag != "native-12" || sel.Version != "0.3.0.12" || !sel.Current(rel) {
		t.Fatalf("selected %+v after update", sel)
	}
	for _, kept := range []string{"saves/game.SAV", "settings.json", "root.txt", "releases/main-297b0734f02f-x-y/nanolathe.exe"} {
		if !exists(in, kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	for _, gone := range []string{"toolchains", "cache", "work-abc", "install.lock", "releases/main-111111111111-x-y"} {
		if exists(in, gone) {
			t.Errorf("%s was kept", gone)
		}
	}

	repo.publish("native-13", true, "0.3.0.13", game(), false)
	rel, _ = src.Find(ctx, Latest, "windows", "amd64")
	if err := in.Update(ctx, src, rel, "windows", "amd64", nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(in.Path("releases"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "0.3.0.12-native-12,0.3.0.13-native-13" {
		t.Fatalf("releases after the second update: %v", names)
	}
}

// TestUpdateRefusesUnverifiedPackages: a package that fails its checksum
// or holds a path is never promoted, and leaves no staging files.
func TestUpdateRefusesUnverifiedPackages(t *testing.T) {
	noStartCheck(t)
	for name, publish := range map[string]func(*fakeRepo){
		"checksum": func(r *fakeRepo) { r.publish("native-5", true, "0.3.0.5", game(), true) },
		"path":     func(r *fakeRepo) { r.publish("native-5", true, "0.3.0.5", game("../escape", "x"), false) },
		"no game":  func(r *fakeRepo) { r.publish("native-5", true, "0.3.0.5", map[string]string{"LICENSE": "MIT"}, false) },
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepo(t)
			publish(repo)
			in := windowsInstall(t)
			sourceInstall(t, in)
			src, ctx := repo.source(), context.Background()
			rel, err := src.Find(ctx, Latest, "windows", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			if err := in.Update(ctx, src, rel, "windows", "amd64", nil); err == nil {
				t.Fatal("update succeeded")
			}
			if cur, _ := in.Current(); cur != "main-297b0734f02f-x-y" {
				t.Fatalf("selection moved to %q", cur)
			}
			entries, _ := os.ReadDir(in.Path("releases"))
			if len(entries) != 2 {
				t.Fatalf("releases left behind: %d entries", len(entries))
			}
			if !exists(in, "toolchains") || !exists(in, "cache") {
				t.Fatal("a failed update removed the working installer's files")
			}
		})
	}
}

// TestRemoveFilesKeepsWhatItDidNotMake: uninstalling from a shared folder
// removes the installation's own files and leaves everything else; the
// player's files go only when asked.
func TestRemoveFilesKeepsWhatItDidNotMake(t *testing.T) {
	in := windowsInstall(t)
	sourceInstall(t, in)
	for path, data := range map[string]string{
		"Nanolathe-Launcher.exe": "MZ", "nanolathe-launcher.exe.old": "MZ", "launcher.json": "{}", "launch.ps1": "",
		"logs/game.log": "", "survival-bests.json": "{}", "launcher.json.123.tmp": "",
		"notes.txt": "mine", "Other Game/game.exe": "MZ", "work-notes.txt": "mine",
	} {
		path = filepath.Join(in.Base, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := []string{"notes.txt", "Other Game/game.exe", "work-notes.txt"}
	player := []string{"saves/game.SAV", "settings.json", "survival-bests.json"}
	if err := in.RemoveFiles(false, in.Path("Nanolathe-Launcher.exe")); err != nil {
		t.Fatal(err)
	}
	for _, kept := range append(append([]string{"Nanolathe-Launcher.exe"}, unrelated...), player...) {
		if !exists(in, kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	if entries, _ := os.ReadDir(in.Base); len(entries) != 7 {
		t.Errorf("%d entries left, want the running launcher, 3 unrelated and 3 player files", len(entries))
	}
	if err := in.RemoveFiles(true, in.Path("Nanolathe-Launcher.exe")); err != nil {
		t.Fatal(err)
	}
	for _, path := range player {
		if exists(in, path) {
			t.Errorf("%s was kept when the player asked to remove it", path)
		}
	}
	for _, kept := range unrelated {
		if !exists(in, kept) {
			t.Errorf("%s was removed with the player's files", kept)
		}
	}
}

// TestSettingsDefaultToLatest: a player who has not chosen follows Latest;
// Stable is kept once chosen.
func TestSettingsDefaultToLatest(t *testing.T) {
	in := windowsInstall(t)
	if s, ok := in.LoadSettings(); ok || s.Channel != Latest {
		t.Fatalf("defaults %+v, chosen %v", s, ok)
	}
	if err := in.SaveSettings(Settings{Channel: Stable}); err != nil {
		t.Fatal(err)
	}
	if s, ok := in.LoadSettings(); !ok || s.Channel != Stable {
		t.Fatalf("saved %+v, chosen %v", s, ok)
	}
	if in.SaveSettings(Settings{Channel: "nightly"}) == nil {
		t.Fatal("unknown channel saved")
	}
}

// TestPackageNamesMatchTheReleaseBuild: the launcher looks for the assets
// tools/native_build.py publishes.
func TestPackageNamesMatchTheReleaseBuild(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "tools", "native_build.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{PackageAsset("windows", "amd64"), PackageAsset("windows", "arm64"),
		PackageAsset("linux", "amd64"), PackageAsset("linux", "arm64"), buildInfoAsset, checksumsAsset} {
		if !bytes.Contains(data, []byte(`"`+name+`"`)) {
			t.Errorf("tools/native_build.py does not publish %s", name)
		}
	}
}
