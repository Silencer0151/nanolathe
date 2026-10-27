package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is the repository whose releases the launcher installs.
const DefaultAPI = "https://api.github.com/repos/nanolathe-gg/nanolathe"

// The assets every native release carries (tools/native_build.py ASSETS).
const (
	buildInfoAsset = "build.json"
	checksumsAsset = "SHA256SUMS"
)

// PackageAsset is the release asset holding one platform's build.
func PackageAsset(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("nanolathe-%s-%s.zip", goos, goarch)
	}
	return fmt.Sprintf("nanolathe-%s-%s.tar.gz", goos, goarch)
}

// ErrNoRelease reports that a channel has no complete release for this
// platform yet, which is the Stable channel's state until a vX.Y.Z release
// carries native builds.
var ErrNoRelease = errors.New("nanolathe: the channel has no release for this platform yet")

// Release is one published build a channel offers.
type Release struct {
	Tag    string
	Name   string
	assets map[string]string // name → download URL
}

// Source finds and downloads releases.
type Source struct {
	API    string
	Client *http.Client
}

// NewSource returns the public repository's source with bounded requests.
// Release assets redirect from GitHub to its download host; only HTTPS
// redirects are followed. NANOLATHE_RELEASES_API names another repository's
// API root (https://api.github.com/repos/<owner>/<name>), to try a fork's
// releases.
func NewSource() Source {
	api := DefaultAPI
	if override := os.Getenv("NANOLATHE_RELEASES_API"); override != "" {
		api = strings.TrimRight(override, "/")
	}
	return Source{API: api, Client: &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 5 {
				return errors.New("nanolathe: refused a release download redirect")
			}
			return nil
		},
	}}
}

type apiRelease struct {
	Tag        string `json:"tag_name"`
	Name       string `json:"name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r apiRelease) release() Release {
	rel := Release{Tag: r.Tag, Name: r.Name, assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel
}

// complete reports whether a release carries what installing on goos/goarch
// needs. GitHub publishes a release's assets with it, but an incomplete
// release is never offered.
func (r Release) complete(goos, goarch string) bool {
	for _, name := range []string{buildInfoAsset, checksumsAsset, PackageAsset(goos, goarch)} {
		if r.assets[name] == "" {
			return false
		}
	}
	return true
}

var (
	stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	latestTag = regexp.MustCompile(`^native-([0-9]+)$`)
)

// Find returns the release a channel offers for goos/goarch: Stable's
// newest published vX.Y.Z release (GitHub's "latest", which skips
// prereleases), Latest's highest-numbered native-<run> prerelease. Release
// order on GitHub need not follow run numbers, so Latest compares them.
func (s Source) Find(ctx context.Context, ch Channel, goos, goarch string) (Release, error) {
	switch ch {
	case Stable:
		var r apiRelease
		if err := s.getJSON(ctx, s.API+"/releases/latest", &r); err != nil {
			if errors.Is(err, errNotFound) {
				return Release{}, ErrNoRelease
			}
			return Release{}, err
		}
		rel := r.release()
		if r.Draft || r.Prerelease || !stableTag.MatchString(r.Tag) || !rel.complete(goos, goarch) {
			return Release{}, ErrNoRelease
		}
		return rel, nil
	case Latest:
		var rs []apiRelease
		if err := s.getJSON(ctx, s.API+"/releases?per_page=50", &rs); err != nil {
			return Release{}, err
		}
		best, bestRun := Release{}, -1
		for _, r := range rs {
			m := latestTag.FindStringSubmatch(r.Tag)
			if m == nil || r.Draft {
				continue
			}
			run, err := strconv.Atoi(m[1])
			if rel := r.release(); err == nil && run > bestRun && rel.complete(goos, goarch) {
				best, bestRun = rel, run
			}
		}
		if bestRun < 0 {
			return Release{}, ErrNoRelease
		}
		return best, nil
	}
	return Release{}, fmt.Errorf("nanolathe: unknown launcher channel %q", ch)
}

var errNotFound = errors.New("not found")

func (s Source) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Nanolathe-launcher")
	req.Header.Set("Accept", accept)
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("nanolathe: %s answered %s", url, resp.Status)
	}
	return resp, nil
}

func (s Source) getJSON(ctx context.Context, url string, v any) error {
	resp, err := s.get(ctx, url, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// small downloads a release's small text asset.
func (s Source) small(ctx context.Context, rel Release, name string) ([]byte, error) {
	resp, err := s.get(ctx, rel.assets[name], "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err == nil && len(data) > 1<<20 {
		err = fmt.Errorf("nanolathe: release asset %s is too large", name)
	}
	return data, err
}

// parseChecksums reads SHA256SUMS: "<hex>  <name>" lines.
func parseChecksums(data []byte) (map[string][32]byte, error) {
	sums := map[string][32]byte{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		hexSum, name, ok := strings.Cut(line, "  ")
		var sum [32]byte
		if decoded, err := hex.DecodeString(hexSum); !ok || err != nil || len(decoded) != 32 || name == "" {
			return nil, fmt.Errorf("nanolathe: malformed SHA256SUMS line %q", line)
		} else {
			copy(sum[:], decoded)
		}
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("nanolathe: SHA256SUMS lists %s twice", name)
		}
		sums[name] = sum
	}
	return sums, scanner.Err()
}

// BuildInfo is a release's build.json (tools/native_build.py).
type BuildInfo struct {
	Schema        int    `json:"schema"`
	Revision      string `json:"revision"`
	Version       string `json:"version"`
	BuildManifest string `json:"build_manifest"`
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func parseBuildInfo(data []byte) (BuildInfo, error) {
	var info BuildInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("nanolathe: unreadable build.json: %w", err)
	}
	if info.Schema != 1 || !revisionPattern.MatchString(info.Revision) || info.Version == "" {
		return info, errors.New("nanolathe: build.json is not a schema 1 build record")
	}
	return info, nil
}
