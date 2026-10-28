package launcher

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// releaseTagFile records, inside a release directory, the published release
// it came from. A source-built release has none.
const releaseTagFile = "release-tag"

// maxPackage bounds a downloaded package; current packages are ~15 MB.
const maxPackage = 1 << 30

// Installed describes the selected release.
type Installed struct {
	Name string
	// Tag is the published release, "" for a source-installer build.
	Tag string
	// Version is the build's version, or the source revision's short form
	// for a source-installer build.
	Version string
	// Revision is the build's full source revision, "" when unknown.
	Revision string
}

// Selected describes the selected release, ok false when none is installed.
func (in Install) Selected() (Installed, bool) {
	name, err := in.Current()
	if err != nil || name == "" || in.GamePath() == "" {
		return Installed{}, false
	}
	dir := in.Path("releases", name)
	sel := Installed{Name: name}
	if tag, err := os.ReadFile(filepath.Join(dir, releaseTagFile)); err == nil {
		sel.Tag = strings.TrimSpace(string(tag))
	}
	if data, err := os.ReadFile(filepath.Join(dir, buildInfoAsset)); err == nil {
		if info, err := parseBuildInfo(data); err == nil {
			sel.Version, sel.Revision = info.Version, info.Revision
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "source-revision")); err == nil {
		rev := strings.TrimSpace(string(data))
		if sel.Version == "" && len(rev) >= 12 {
			sel.Version = "main-" + rev[:12]
		}
		if sel.Revision == "" && revisionPattern.MatchString(rev) {
			sel.Revision = rev
		}
	}
	return sel, true
}

// Current reports whether the selected release is rel.
func (s Installed) Current(rel Release) bool { return s.Tag != "" && s.Tag == rel.Tag }

// Progress reports a download's bytes so far and its total, or -1 when the
// total is unknown.
type Progress func(done, total int64)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Update installs rel for goos/goarch and selects it. Every byte is checked
// against the release's SHA256SUMS before use, a release older than the
// installed build is refused with ErrBackward (Install.Offer), the package
// is unpacked into a staging directory, and its game must start (--help)
// before the release is promoted; any failure leaves the previous
// selection untouched. After
// promotion, releases other than the new and the previous one are pruned,
// and the source installers' compiler and build cache are removed.
func (in Install) Update(ctx context.Context, src Source, rel Release, goos, goarch string, progress Progress) error {
	if !rel.complete(goos, goarch) {
		return ErrNoRelease
	}
	releases := in.Path("releases")
	for _, dir := range []string{releases, in.Path("logs"), in.Path("saves")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	rec, err := src.buildRecord(ctx, rel)
	if err != nil {
		return err
	}
	if st, err := in.standing(ctx, src, rel, &rec.info); err != nil {
		return err
	} else if st == Backward {
		return ErrBackward
	}
	sums, infoData, info := rec.sums, rec.data, rec.info
	name := unsafeName.ReplaceAllString(info.Version+"-"+rel.Tag, "_")
	previous, _ := in.Current()
	if dir := filepath.Join(releases, name); in.complete(dir, rel.Tag) {
		return in.promote(name, previous)
	}

	pkg := PackageAsset(goos, goarch)
	download, err := os.CreateTemp(releases, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(download.Name())
	sum, err := src.download(ctx, rel, pkg, download, progress)
	if cerr := download.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := checkSum(sums, pkg, sum); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(releases, ".stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if strings.HasSuffix(pkg, ".zip") {
		err = unzip(download.Name(), stage)
	} else {
		err = untar(download.Name(), stage)
	}
	if err != nil {
		return err
	}
	exe := filepath.Join(stage, in.Executable())
	if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("nanolathe: release %s has no %s", rel.Tag, in.Executable())
	}
	if !in.windows() {
		if err := os.Chmod(exe, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, buildInfoAsset), infoData, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, releaseTagFile), []byte(rel.Tag+"\n"), 0o644); err != nil {
		return err
	}
	if err := startCheck(ctx, exe); err != nil {
		return err
	}
	dest := filepath.Join(releases, name)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := renameSettled(stage, dest); err != nil {
		return err
	}
	return in.promote(name, previous)
}

// renameSettled renames a freshly written directory. On Windows a virus
// scanner reading a new executable, or a just-exited check of it, can hold
// the directory open for a moment, so a refused rename is retried briefly.
func renameSettled(from, to string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

// complete reports whether dir already holds the release tagged tag.
func (in Install) complete(dir, tag string) bool {
	data, err := os.ReadFile(filepath.Join(dir, releaseTagFile))
	if err != nil || strings.TrimSpace(string(data)) != tag {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, in.Executable()))
	return err == nil && info.Mode().IsRegular()
}

// promote selects name, then prunes and cleans up. Cleanup failures (an
// older build still running holds its files) are retried after the next
// update rather than reported.
func (in Install) promote(name, previous string) error {
	if err := in.selectRelease(name); err != nil {
		return err
	}
	in.prune(name, previous)
	in.removeSourceInstallerFiles()
	return nil
}

// prune removes releases other than keep, and stale staging files.
func (in Install) prune(keep ...string) {
	entries, err := os.ReadDir(in.Path("releases"))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".") && (!e.IsDir() || containsString(keep, name)) {
			continue
		}
		_ = os.RemoveAll(in.Path("releases", name))
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v != "" && v == s {
			return true
		}
	}
	return false
}

// removeSourceInstallerFiles deletes what only the source installers use
// (sourceInstallerFiles), with their interrupted work and stage
// directories. Saves, settings, logs and the remembered game folder stay.
func (in Install) removeSourceInstallerFiles() {
	for _, name := range sourceInstallerFiles {
		_ = os.RemoveAll(in.Path(name))
	}
	entries, err := os.ReadDir(in.Base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && (strings.HasPrefix(e.Name(), "work-") || strings.HasPrefix(e.Name(), "stage-")) {
			_ = os.RemoveAll(in.Path(e.Name()))
		}
	}
}

func checkSum(sums map[string][32]byte, name string, got [32]byte) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("nanolathe: SHA256SUMS does not list %s", name)
	}
	if got != want {
		return fmt.Errorf("nanolathe: SHA-256 verification failed for %s", name)
	}
	return nil
}

// download copies an asset into w, hashing it, within maxPackage.
func (s Source) download(ctx context.Context, rel Release, name string, w io.Writer, progress Progress) ([32]byte, error) {
	var sum [32]byte
	resp, err := s.get(ctx, rel.assets[name], "application/octet-stream")
	if err != nil {
		return sum, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxPackage {
		return sum, fmt.Errorf("nanolathe: release asset %s is too large", name)
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, progress: progress}
	n, err := io.Copy(io.MultiWriter(w, h, pw), io.LimitReader(resp.Body, maxPackage+1))
	if err != nil {
		return sum, err
	}
	if n > maxPackage {
		return sum, fmt.Errorf("nanolathe: release asset %s is too large", name)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return sum, fmt.Errorf("nanolathe: release asset %s ended early", name)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

type progressWriter struct {
	done, total int64
	progress    Progress
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.progress != nil && (time.Since(p.last) > 50*time.Millisecond || p.done == p.total) {
		p.last = time.Now()
		p.progress(p.done, p.total)
	}
	return len(b), nil
}

// packageFile admits one archive entry: a regular file directly in the
// package, never a path, link or device.
func packageFile(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\:`) || name == "." || name == ".." || unsafeName.MatchString(name) {
		return "", fmt.Errorf("nanolathe: release package entry %q refused", name)
	}
	return name, nil
}

func unzip(archive, dest string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		name, err := packageFile(f.Name)
		if err != nil || !f.Mode().IsRegular() {
			return fmt.Errorf("nanolathe: release package entry %q refused", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(filepath.Join(dest, name), r, int64(f.UncompressedSize64))
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untar(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := packageFile(h.Name)
		if err != nil || h.Typeflag != tar.TypeReg {
			return fmt.Errorf("nanolathe: release package entry %q refused", h.Name)
		}
		if err := writeFile(filepath.Join(dest, name), tr, h.Size); err != nil {
			return err
		}
	}
}

func writeFile(path string, r io.Reader, size int64) error {
	if size > maxPackage {
		return fmt.Errorf("nanolathe: release package entry %s is too large", filepath.Base(path))
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(r, size))
	return errors.Join(err, out.Close())
}

// startCheck is checkStarts; tests replace it, having no real game.
var startCheck = checkStarts

// checkStarts runs a staged game's --help, which needs no game data or
// window, as the source installers do before promotion.
func checkStarts(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--help")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nanolathe: the downloaded game did not start: %w", err)
	}
	return nil
}

// InstalledLauncher is the launcher's path inside the installation.
func (in Install) InstalledLauncher() string { return in.Path(in.LauncherExecutable()) }

// InstallLauncher copies the running launcher into the installation. A
// launcher already there may be running; Windows lets a running program be
// renamed but not replaced, so it is moved aside first.
func (in Install) InstallLauncher(running string) error {
	if err := os.MkdirAll(in.Base, 0o755); err != nil {
		return err
	}
	return in.replaceLauncher(running)
}

// UpdateLauncher installs the launcher the selected release ships when it
// differs from the installed one, and reports whether it did. The running
// launcher keeps working; the next start uses the new one.
func (in Install) UpdateLauncher() (bool, error) {
	name, err := in.Current()
	if err != nil || name == "" {
		return false, err
	}
	shipped := in.Path("releases", name, in.LauncherExecutable())
	next, err := os.ReadFile(shipped)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if current, err := os.ReadFile(in.InstalledLauncher()); err == nil && bytes.Equal(current, next) {
		return false, nil
	}
	return true, in.replaceLauncher(shipped)
}

func (in Install) replaceLauncher(from string) error {
	dest := in.InstalledLauncher()
	if same, _ := sameFile(from, dest); same {
		return nil
	}
	old := dest + ".old"
	_ = os.Remove(old)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := copyFile(from, dest); err != nil {
		_ = os.Rename(old, dest)
		return err
	}
	return nil
}

// RemoveOldLauncher deletes a launcher moved aside by an earlier update,
// once nothing runs it.
func (in Install) RemoveOldLauncher() { _ = os.Remove(in.InstalledLauncher() + ".old") }

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(to), filepath.Base(to)+".*.tmp")
	if err != nil {
		return err
	}
	_, cerr := io.Copy(tmp, in)
	if err := errors.Join(cerr, tmp.Chmod(0o755), tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), to); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
