package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const GitHubAPIPath = "https://api.github.com/repos/qase-tms/qase-tunnel/releases/latest"

var (
	ErrNoUpdate          = errors.New("update: already at latest version")
	ErrSignatureMismatch = errors.New("update: cosign signature verification failed")
	ErrChecksumMismatch  = errors.New("update: checksum mismatch")
	ErrRateLimited       = errors.New("update: rate limited by github")
)

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Checker interface {
	Latest(ctx context.Context) (Release, error)
}

type Downloader interface {
	Download(ctx context.Context, url string) (io.ReadCloser, error)
}

type Verifier interface {
	Verify(artifact io.Reader, signatureURL string) error
}

type Swapper interface {
	Swap(targetPath string, artifact io.Reader) error
}

type Updater struct {
	BuildVersion string
	BinaryPath   string
	Checker      Checker
	Downloader   Downloader
	Verifier     Verifier
	Swapper      Swapper
	Disabled     bool
	AssetMatcher func(Asset) bool
	SigSuffix    string
}

func NewUpdater(buildVersion, binaryPath string, c Checker, d Downloader, v Verifier, s Swapper) *Updater {
	return &Updater{
		BuildVersion: buildVersion,
		BinaryPath:   binaryPath,
		Checker:      c,
		Downloader:   d,
		Verifier:     v,
		Swapper:      s,
		AssetMatcher: nil,
		SigSuffix:    ".sig",
	}
}

func (u *Updater) Run(ctx context.Context) error {
	if u.Disabled {
		return ErrNoUpdate
	}

	rel, err := u.Checker.Latest(ctx)
	if err != nil {
		return err
	}

	if !isNewer(u.BuildVersion, rel.TagName) {
		return ErrNoUpdate
	}

	asset, sigAsset, err := u.pickAssets(rel)
	if err != nil {
		return err
	}

	rc, err := u.Downloader.Download(ctx, asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "qase-tunnel-update-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, rc); err != nil {
		return fmt.Errorf("write artifact tmp: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	sigURL := ""
	if sigAsset != nil {
		sigURL = sigAsset.BrowserDownloadURL
	}
	if err := u.Verifier.Verify(tmp, sigURL); err != nil {
		return ErrSignatureMismatch
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if err := u.Swapper.Swap(u.BinaryPath, tmp); err != nil {
		return fmt.Errorf("swap binary: %w", err)
	}
	return nil
}

func (u *Updater) pickAssets(rel Release) (artifact Asset, sig *Asset, err error) {
	matcher := u.AssetMatcher
	if matcher == nil {
		matcher = func(a Asset) bool {
			lower := strings.ToLower(a.Name)
			return !strings.HasSuffix(lower, ".sig") &&
				!strings.HasSuffix(lower, ".sha256")
		}
	}

	suffix := u.SigSuffix
	if suffix == "" {
		suffix = ".sig"
	}

	for _, a := range rel.Assets {
		if matcher(a) {
			artifact = a
			break
		}
	}
	if artifact.Name == "" {
		return Asset{}, nil, fmt.Errorf("no asset matched current OS/arch")
	}
	wanted := artifact.Name + suffix
	for i := range rel.Assets {
		if rel.Assets[i].Name == wanted {
			s := rel.Assets[i]
			return artifact, &s, nil
		}
	}
	return artifact, nil, nil
}

func isNewer(current, latest string) bool {
	c := normalizeVersion(current)
	l := normalizeVersion(latest)
	return semver.Compare(l, c) > 0
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "v0.0.0"
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

type HTTPChecker struct {
	URL    string
	Client *http.Client
}

func NewHTTPChecker() *HTTPChecker {
	return &HTTPChecker{URL: GitHubAPIPath, Client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *HTTPChecker) Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return Release{}, ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return Release{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	return rel, nil
}

type FileSwapper struct{}

func (FileSwapper) Swap(target string, src io.Reader) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".qase-tunnel-new-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), target)
}
