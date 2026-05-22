package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubChecker returns a scripted Release / error.
type stubChecker struct {
	rel  Release
	err  error
	hits atomic.Int64
}

func (s *stubChecker) Latest(_ context.Context) (Release, error) {
	s.hits.Add(1)
	return s.rel, s.err
}

// memDownloader maps URL → bytes.
type memDownloader struct {
	files map[string][]byte
	err   error
}

func (m *memDownloader) Download(_ context.Context, url string) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	body, ok := m.files[url]
	if !ok {
		return nil, fmt.Errorf("memDownloader: no file for %s", url)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// boolVerifier returns the same outcome on every call.
type boolVerifier struct {
	ok    bool
	calls atomic.Int64
}

func (v *boolVerifier) Verify(_ io.Reader, _ string) error {
	v.calls.Add(1)
	if v.ok {
		return nil
	}
	return errors.New("signature invalid")
}

// captureSwapper records what was swapped into target.
type captureSwapper struct {
	mu      sync.Mutex
	target  string
	content []byte
	err     error
}

func (s *captureSwapper) Swap(target string, src io.Reader) error {
	if s.err != nil {
		return s.err
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.target = target
	s.content = body
	s.mu.Unlock()
	return nil
}

func relWithAssets(tag string) Release {
	return Release{
		TagName: tag,
		Assets: []Asset{
			{Name: "qase-tunnel-linux-amd64", BrowserDownloadURL: "https://example/qase-tunnel-linux-amd64"},
			{Name: "qase-tunnel-linux-amd64.sig", BrowserDownloadURL: "https://example/qase-tunnel-linux-amd64.sig"},
		},
	}
}

func newUpdater(version string, checker Checker, dl Downloader, ver Verifier, sw Swapper) *Updater {
	u := NewUpdater(version, "/tmp/qase-tunnel", checker, dl, ver, sw)
	u.AssetMatcher = func(a Asset) bool { return a.Name == "qase-tunnel-linux-amd64" }
	return u
}

func TestUpdater_CurrentVersionEqualsLatestNoUpdate(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v1.2.3")}
	u := newUpdater("v1.2.3", c, nil, nil, nil)
	err := u.Run(context.Background())
	assert.ErrorIs(t, err, ErrNoUpdate)
}

func TestUpdater_CurrentVersionAheadNoUpdate(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v1.2.0")}
	u := newUpdater("v2.0.0", c, nil, nil, nil)
	err := u.Run(context.Background())
	assert.ErrorIs(t, err, ErrNoUpdate)
}

func TestUpdater_CurrentVersionBehindHappySwap(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v2.0.0")}
	dl := &memDownloader{files: map[string][]byte{
		"https://example/qase-tunnel-linux-amd64":     []byte("NEW BINARY BYTES"),
		"https://example/qase-tunnel-linux-amd64.sig": []byte("sig"),
	}}
	v := &boolVerifier{ok: true}
	sw := &captureSwapper{}

	u := newUpdater("v1.0.0", c, dl, v, sw)
	require.NoError(t, u.Run(context.Background()))

	assert.Equal(t, "/tmp/qase-tunnel", sw.target)
	assert.Equal(t, []byte("NEW BINARY BYTES"), sw.content)
	assert.Equal(t, int64(1), v.calls.Load())
}

func TestUpdater_SignatureFailureAborts(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v2.0.0")}
	dl := &memDownloader{files: map[string][]byte{
		"https://example/qase-tunnel-linux-amd64":     []byte("BAD"),
		"https://example/qase-tunnel-linux-amd64.sig": []byte("sig"),
	}}
	v := &boolVerifier{ok: false}
	sw := &captureSwapper{}

	u := newUpdater("v1.0.0", c, dl, v, sw)
	err := u.Run(context.Background())
	assert.ErrorIs(t, err, ErrSignatureMismatch)
	assert.Empty(t, sw.target, "must not swap on signature failure")
}

func TestUpdater_NetworkFailureOnCheckBubblesUp(t *testing.T) {
	c := &stubChecker{err: errors.New("dial timeout")}
	u := newUpdater("v1.0.0", c, nil, nil, nil)
	err := u.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial timeout")
}

func TestUpdater_RateLimitedSurfacesAsErrRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := &HTTPChecker{URL: srv.URL, Client: srv.Client()}
	_, err := c.Latest(context.Background())
	assert.ErrorIs(t, err, ErrRateLimited)
}

func TestUpdater_DownloadFailureBubblesUp(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v2.0.0")}
	dl := &memDownloader{err: errors.New("connection reset")}
	v := &boolVerifier{ok: true}
	sw := &captureSwapper{}

	u := newUpdater("v1.0.0", c, dl, v, sw)
	err := u.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "download artifact")
}

func TestUpdater_DisabledFlagSkips(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v9.9.9")}
	u := newUpdater("v1.0.0", c, nil, nil, nil)
	u.Disabled = true

	err := u.Run(context.Background())
	assert.ErrorIs(t, err, ErrNoUpdate)
	assert.Equal(t, int64(0), c.hits.Load(), "disabled must short-circuit before HTTP check")
}

func TestUpdater_SwapFailureSurfacesError(t *testing.T) {
	c := &stubChecker{rel: relWithAssets("v2.0.0")}
	dl := &memDownloader{files: map[string][]byte{
		"https://example/qase-tunnel-linux-amd64":     []byte("BIN"),
		"https://example/qase-tunnel-linux-amd64.sig": []byte("sig"),
	}}
	v := &boolVerifier{ok: true}
	sw := &captureSwapper{err: errors.New("disk full")}

	u := newUpdater("v1.0.0", c, dl, v, sw)
	err := u.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "swap")
}

func TestIsNewer_HandlesLeadingV(t *testing.T) {
	assert.True(t, isNewer("1.0.0", "v1.0.1"))
	assert.True(t, isNewer("v1.0.0", "1.0.1"))
	assert.False(t, isNewer("v2.0.0", "v1.99.0"))
	assert.False(t, isNewer("v1.0.0", "v1.0.0"))
	assert.True(t, isNewer("", "v0.0.1"))
}

func TestFileSwapper_AtomicWriteCreatesFileWithExpectedContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "qase-tunnel")
	require.NoError(t, FileSwapper{}.Swap(target, bytes.NewReader([]byte("HELLO"))))

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "HELLO", string(data))

	stat, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, stat.Mode().Perm()&0o100 != 0, "binary must be executable")
}

func TestHTTPChecker_DecodesReleaseFromGitHubLikePayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "tag_name": "v3.4.5",
  "assets": [
    {"name": "qase-tunnel-darwin-arm64", "browser_download_url": "https://x/dl/dar"},
    {"name": "qase-tunnel-darwin-arm64.sig", "browser_download_url": "https://x/dl/dar.sig"}
  ]
}`))
	}))
	defer srv.Close()

	c := &HTTPChecker{URL: srv.URL, Client: srv.Client()}
	rel, err := c.Latest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v3.4.5", rel.TagName)
	require.Len(t, rel.Assets, 2)
	assert.True(t, strings.HasSuffix(rel.Assets[1].Name, ".sig"))
}

