package frpc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureInputs(transport Transport) Inputs {
	return Inputs{
		Token:     "test-token-1234567890",
		AgentName: "tunnel-abcdef12",
		Secret:    "test-secret-base64-32-bytes",
		Transport: transport,
	}
}

func TestRender_TCPMatchesGoldenFile(t *testing.T) {
	got, err := Render(fixtureInputs(TransportTCP))
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("testdata", "golden_tcp.toml"))
	require.NoError(t, err)

	assert.Equal(t, string(want), got)
}

func TestRender_QUICMatchesGoldenFile(t *testing.T) {
	got, err := Render(fixtureInputs(TransportQUIC))
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("testdata", "golden_quic.toml"))
	require.NoError(t, err)

	assert.Equal(t, string(want), got)
}

func TestRender_DefaultIsTCPPort7000(t *testing.T) {
	out, err := Render(fixtureInputs(TransportTCP))
	require.NoError(t, err)
	assert.Contains(t, out, "serverPort = 7000")
	assert.Contains(t, out, `transport.protocol = "tcp"`)
}

func TestRender_QUICUsesPort7002(t *testing.T) {
	out, err := Render(fixtureInputs(TransportQUIC))
	require.NoError(t, err)
	assert.Contains(t, out, "serverPort = 7002")
	assert.Contains(t, out, `transport.protocol = "quic"`)
}

func TestRender_TLSAlwaysOff(t *testing.T) {
	for _, transport := range []Transport{TransportTCP, TransportQUIC} {
		out, err := Render(fixtureInputs(transport))
		require.NoError(t, err)
		assert.Contains(t, out, "transport.tls.enable = false")
	}
}

func TestRender_HasUseEncryptionAndUseCompression(t *testing.T) {
	out, err := Render(fixtureInputs(TransportTCP))
	require.NoError(t, err)
	assert.Contains(t, out, "transport.useEncryption = true")
	assert.Contains(t, out, "transport.useCompression = true")
}

func TestRender_DefaultLogLevelIsInfo(t *testing.T) {
	out, err := Render(fixtureInputs(TransportTCP))
	require.NoError(t, err)
	assert.Contains(t, out, `log.level = "info"`)
	assert.NotContains(t, out, `log.level = "debug"`)
}

func TestRender_DebugInputsEmitsDebugLogLevel(t *testing.T) {
	in := fixtureInputs(TransportTCP)
	in.Debug = true
	out, err := Render(in)
	require.NoError(t, err)
	assert.Contains(t, out, `log.level = "debug"`)
	assert.NotContains(t, out, `log.level = "info"`)
}

func TestRender_HttpProxyPluginHasNoUserOrPassword(t *testing.T) {
	out, err := Render(fixtureInputs(TransportTCP))
	require.NoError(t, err)
	assert.NotContains(t, out, "httpUser")
	assert.NotContains(t, out, "httpPasswd")
}

func TestRender_InvalidTransportRejected(t *testing.T) {
	_, err := Render(Inputs{
		Token: "t", AgentName: "a", Secret: "s",
		Transport: Transport("websocket"),
	})
	assert.ErrorIs(t, err, ErrInvalidTransport)
}

func TestRender_EmptyRequiredFieldsRejected(t *testing.T) {
	cases := []Inputs{
		{Token: "", AgentName: "a", Secret: "s", Transport: TransportTCP},
		{Token: "t", AgentName: "", Secret: "s", Transport: TransportTCP},
		{Token: "t", AgentName: "a", Secret: "", Transport: TransportTCP},
	}
	for _, in := range cases {
		_, err := Render(in)
		assert.Error(t, err)
	}
}

func TestRedact_StripsTokenAndSecret(t *testing.T) {
	in := fixtureInputs(TransportTCP)
	out, err := Render(in)
	require.NoError(t, err)

	redacted := Redact(out, in)
	assert.NotContains(t, redacted, in.Token)
	assert.NotContains(t, redacted, in.Secret)
	assert.Contains(t, redacted, "[REDACTED-TOKEN]")
	assert.Contains(t, redacted, "[REDACTED-SECRET]")
}

func TestWriteTemp_CreatesFileWith0600AndCleansUp(t *testing.T) {
	in := fixtureInputs(TransportTCP)
	path, cleanup, err := WriteTemp(in)
	require.NoError(t, err)

	stat, err := os.Stat(path)
	require.NoError(t, err)

	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), stat.Mode().Perm(), "TOML file must be 0600")
	}

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), in.Token)

	cleanup()
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "cleanup must remove the file")
}

func TestWriteTemp_FileLivesUnderTempDirAndQaseTunnelSubdir(t *testing.T) {
	in := fixtureInputs(TransportTCP)
	path, cleanup, err := WriteTemp(in)
	require.NoError(t, err)
	defer cleanup()

	assert.True(t, strings.HasPrefix(path, os.TempDir()) ||
		strings.HasPrefix(path, filepath.Join(os.TempDir(), "qase-tunnel")),
		"file should live under temp dir, got %s", path)
}
