package frpc

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureWriter struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureWriter) WriteLine(l string) {
	c.mu.Lock()
	c.lines = append(c.lines, l)
	c.mu.Unlock()
}

func (c *captureWriter) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func (c *captureWriter) joined() string {
	return strings.Join(c.snapshot(), "\n")
}

// shellSpawner returns a Spawner that runs the supplied script via /bin/sh -c.
// Skips macOS/Linux gating to keep tests cross-platform — Windows CI will
// substitute powershell if/when these tests get ported there.
func shellSpawner(script string) Spawner {
	return func(ctx context.Context, _ string, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based fake frpc not available on Windows in unit tests")
	}
}

func newLifecycle(spawner Spawner, out LineWriter, in Inputs) *Lifecycle {
	return New(Options{
		Binary:      "fake-frpc",
		ConfigPath:  "fake.toml",
		Spawner:     spawner,
		Output:      out,
		Inputs:      in,
		MaxRestarts: 2,
		BaseBackoff: 10 * time.Millisecond,
	})
}

func TestLifecycle_HappyExitsCleanlyAndCapturesStdout(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := newLifecycle(
		shellSpawner(`printf 'line-a\nline-b\nline-c\n'`),
		out,
		Inputs{},
	)

	err := l.Run(context.Background())
	require.NoError(t, err)
	assert.Contains(t, out.joined(), "line-a")
	assert.Contains(t, out.joined(), "line-b")
	assert.Contains(t, out.joined(), "line-c")
}

func TestLifecycle_DropsPerRequestPluginNoise(t *testing.T) {
	skipOnWindows(t)

	// Three lines frpc emits per work-connection at debug level. We keep the
	// `start a new work connection` line (one signal per request) and drop
	// both `handle by plugin` companions.
	script := `printf '%s\n%s\n%s\n%s\n' \
		'2026-05-13 16:03:32.592 [D] start a new work connection, localAddr: 127.0.0.1:54125 remoteAddr: 127.0.0.1:17000' \
		'2026-05-13 16:03:32.599 [D] handle by plugin: http_proxy' \
		'2026-05-13 16:03:32.600 [D] handle by plugin finished' \
		'2026-05-13 16:03:32.700 [I] login to server success'`

	out := &captureWriter{}
	l := newLifecycle(shellSpawner(script), out, Inputs{})

	err := l.Run(context.Background())
	require.NoError(t, err)

	joined := out.joined()
	assert.Contains(t, joined, "start a new work connection")
	assert.Contains(t, joined, "login to server success")
	assert.NotContains(t, joined, "handle by plugin")
}

func TestLifecycle_StderrLinesGetTranslatedBanners(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := newLifecycle(
		shellSpawner(`echo 'wsarecv: forcibly closed by remote host' 1>&2; exit 0`),
		out,
		Inputs{},
	)

	err := l.Run(context.Background())
	require.NoError(t, err)

	joined := out.joined()
	assert.Contains(t, joined, "wsarecv")
	assert.Contains(t, joined, "Endpoint security")
	assert.Contains(t, joined, "#wsarecv")
}

func TestLifecycle_NonZeroExitTriggersBackoffRestart(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := New(Options{
		Binary:      "fake",
		Spawner:     shellSpawner(`echo 'crash' 1>&2; exit 1`),
		Output:      out,
		MaxRestarts: 2,
		BaseBackoff: 5 * time.Millisecond,
	})

	err := l.Run(context.Background())
	assert.Error(t, err)

	crashes := strings.Count(out.joined(), "crash")
	assert.GreaterOrEqual(t, crashes, 3, "should have spawned 1 + 2 retries = 3 crashes; got %d", crashes)
}

func TestLifecycle_CleanExitDoesNotRestart(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	calls := 0
	wrapped := func(ctx context.Context, _, _ string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'ok\n'; exit 0`)
	}

	l := New(Options{
		Binary:      "fake",
		Spawner:     wrapped,
		Output:      out,
		MaxRestarts: 5,
		BaseBackoff: 5 * time.Millisecond,
	})

	require.NoError(t, l.Run(context.Background()))
	assert.Equal(t, 1, calls, "clean exit must not restart")
}

func TestLifecycle_KillOnContextCancel(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := New(Options{
		Binary:      "fake",
		Spawner:     shellSpawner(`echo running; exec sleep 30`),
		Output:      out,
		MaxRestarts: 0,
		BaseBackoff: 5 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	require.Eventually(t, func() bool { return strings.Contains(out.joined(), "running") }, 2*time.Second, 10*time.Millisecond)

	stop := time.Now()
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
		assert.Less(t, time.Since(stop), 5*time.Second, "ctx cancel must terminate within 5s")
	case <-time.After(5 * time.Second):
		t.Fatal("Lifecycle.Run did not return after ctx cancel")
	}
}

func TestLifecycle_TokenAndSecretNeverInOutput(t *testing.T) {
	skipOnWindows(t)

	const token = "AAA-DO-NOT-LEAK-TOKEN"
	const secret = "BBB-DO-NOT-LEAK-SECRET"

	out := &captureWriter{}
	l := New(Options{
		Binary: "fake",
		Spawner: shellSpawner(
			`echo "config has token=` + token + ` and secret=` + secret + `"; exit 0`,
		),
		Output:      out,
		Inputs:      Inputs{Token: token, Secret: secret},
		MaxRestarts: 0,
	})

	require.NoError(t, l.Run(context.Background()))

	joined := out.joined()
	assert.NotContains(t, joined, token)
	assert.NotContains(t, joined, secret)
	assert.Contains(t, joined, "[REDACTED-TOKEN]")
	assert.Contains(t, joined, "[REDACTED-SECRET]")
}

func TestLifecycle_PidExposedAfterStart(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := New(Options{
		Binary:      "fake",
		Spawner:     shellSpawner(`sleep 0.05; exit 0`),
		Output:      out,
		MaxRestarts: 0,
	})
	require.NoError(t, l.Run(context.Background()))
	assert.Greater(t, l.Pid(), 0)
}

func TestLifecycle_StcpSecretMismatchInStderrTranslates(t *testing.T) {
	skipOnWindows(t)

	out := &captureWriter{}
	l := New(Options{
		Binary:      "fake",
		Spawner:     shellSpawner(`echo 'stcp visitor secret invalid' 1>&2; exit 1`),
		Output:      out,
		MaxRestarts: 0,
		BaseBackoff: 5 * time.Millisecond,
	})

	err := l.Run(context.Background())
	assert.Error(t, err) // exit 1 + max-restarts 0 → wrapped error
	joined := out.joined()
	assert.Contains(t, joined, "Tunnel secret rotated")
	assert.Contains(t, joined, "#stcp-secret-mismatch")
}
