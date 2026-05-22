package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/qase-tms/qase-tunnel/internal/api"
	"github.com/qase-tms/qase-tunnel/internal/keystore"
	"github.com/qase-tms/qase-tunnel/internal/transport/frpc"
	"github.com/qase-tms/qase-tunnel/internal/wizard"
)

type recordingRunner struct {
	mu     sync.Mutex
	calls  atomic.Int64
	inputs []frpc.Inputs
	err    error
	block  bool
}

func (r *recordingRunner) Run(ctx context.Context, in frpc.Inputs) error {
	r.calls.Add(1)
	r.mu.Lock()
	r.inputs = append(r.inputs, in)
	r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.block {
		<-ctx.Done()
	}
	return nil
}

func (r *recordingRunner) snapshot() []frpc.Inputs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]frpc.Inputs(nil), r.inputs...)
}

type fakeRegistrar struct {
	calls   int
	results []registerResult
}

type registerResult struct {
	tunnel api.Tunnel
	err    error
}

func (f *fakeRegistrar) RegisterTunnel(_ context.Context, _ string) (api.Tunnel, error) {
	f.calls++
	if len(f.results) == 0 {
		return api.Tunnel{}, errors.New("fakeRegistrar: out of scripted results")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.tunnel, r.err
}

// ListTunnels reports zero tunnels by default so legacy tests that don't
// pre-seed a keystore UUID still hit the register branch in acquireTunnel.
func (f *fakeRegistrar) ListTunnels(_ context.Context, _ string) ([]api.TunnelListItem, error) {
	return nil, nil
}

func (f *fakeRegistrar) RotateTunnel(_ context.Context, _, _ string) (api.Tunnel, error) {
	if len(f.results) == 0 {
		return api.Tunnel{}, errors.New("fakeRegistrar: out of scripted results")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.tunnel, r.err
}

// Heartbeat is a no-op; tests that need to assert heartbeat behavior wrap
// fakeRegistrar with their own thin recorder.
func (f *fakeRegistrar) Heartbeat(_ context.Context, _, _ string) error {
	return nil
}

type scriptedPrompt struct {
	tokens []string
	tokErr error
}

func (p *scriptedPrompt) AskToken(_ context.Context) (string, error) {
	if p.tokErr != nil {
		return "", p.tokErr
	}
	if len(p.tokens) == 0 {
		return "", io.EOF
	}
	t := p.tokens[0]
	p.tokens = p.tokens[1:]
	return t, nil
}

func (p *scriptedPrompt) AskRetry(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func newDeps(reg *fakeRegistrar, ks keystore.Keystore, prompt wizard.Prompt) (Deps, *bytes.Buffer, *bytes.Buffer) {
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	return Deps{
		APIClient: reg,
		Keystore:  ks,
		Prompt:    prompt,
		Stdout:    out,
		Stderr:    errBuf,
	}, out, errBuf
}

func okTunnel() api.Tunnel {
	return api.Tunnel{UUID: "u-1234", AgentName: "tunnel-abcdef12", Secret: "s-base64"}
}

func TestStart_HappyPersistsCredentialsAndPrintsConfirmation(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	deps, out, _ := newDeps(reg, ks, nil)

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitOK, code)

	tok, _ := ks.Get(KeyAPIToken)
	uuid, _ := ks.Get(KeyTunnelUUID)
	agent, _ := ks.Get(KeyAgentName)
	secret, _ := ks.Get(KeyStcpSecret)
	tr, _ := ks.Get(KeyTransport)
	assert.Equal(t, "valid-token-1234567890", tok)
	assert.Equal(t, "u-1234", uuid)
	assert.Equal(t, "tunnel-abcdef12", agent)
	assert.Equal(t, "s-base64", secret)
	assert.Equal(t, "tcp", tr)
	assert.Contains(t, out.String(), "tunnel-abcdef12")
}

func TestStart_DebugFlagPropagatesToRunnerInputs(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{}
	deps, _, _ := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"--debug", "start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitOK, code)

	got := runner.snapshot()
	require.Len(t, got, 1)
	assert.True(t, got[0].Debug, "Inputs.Debug must be true when --debug is set at root")
}

func TestStart_NoDebugFlagDefaultsToFalse(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{}
	deps, _, _ := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitOK, code)

	got := runner.snapshot()
	require.Len(t, got, 1)
	assert.False(t, got[0].Debug, "Inputs.Debug must default to false")
}

func TestResume_DebugFlagPropagatesToRunnerInputs(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	require.NoError(t, ks.Set(KeyAPIToken, "saved-token-1234567890"))
	require.NoError(t, ks.Set(KeyTunnelUUID, "existing-uuid"))
	require.NoError(t, ks.Set(KeyAgentName, "tunnel-existing"))
	require.NoError(t, ks.Set(KeyStcpSecret, "existing-secret"))
	require.NoError(t, ks.Set(KeyTransport, "tcp"))

	runner := &recordingRunner{}
	deps, _, _ := newDeps(&fakeRegistrar{}, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"--debug"}, deps)
	assert.Equal(t, ExitOK, code)

	got := runner.snapshot()
	require.Len(t, got, 1)
	assert.True(t, got[0].Debug, "resume path must honor --debug too")
}

func TestStart_QUICTransportPersisted(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	deps, _, _ := newDeps(reg, ks, nil)

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890", "--transport", "quic"}, deps)
	assert.Equal(t, ExitOK, code)

	tr, _ := ks.Get(KeyTransport)
	assert.Equal(t, "quic", tr)
}

func TestStart_InvalidTransportExits2(t *testing.T) {
	reg := &fakeRegistrar{}
	ks := keystore.NewMemoryKeystore()
	deps, _, errBuf := newDeps(reg, ks, nil)

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890", "--transport", "websocket"}, deps)
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, errBuf.String(), "websocket")
	assert.Equal(t, 0, reg.calls, "API must not be called when flags are invalid")
}

func TestStart_MissingTokenExits2(t *testing.T) {
	reg := &fakeRegistrar{}
	ks := keystore.NewMemoryKeystore()
	deps, _, errBuf := newDeps(reg, ks, nil)

	code := Run(context.Background(), []string{"start"}, deps)
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, errBuf.String(), "api-token")
}

func TestStart_UnauthorizedReturnsGenericError(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{err: api.ErrUnauthorized}}}
	ks := keystore.NewMemoryKeystore()
	deps, _, errBuf := newDeps(reg, ks, nil)

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitGeneric, code)
	assert.NotEmpty(t, errBuf.String())
}

func TestDefault_FreshInstallTriggersWizard(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	prompt := &scriptedPrompt{tokens: []string{"valid-token-1234567890"}}
	deps, out, _ := newDeps(reg, ks, prompt)

	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, 1, reg.calls, "wizard must call API once")

	tok, _ := ks.Get(KeyAPIToken)
	assert.Equal(t, "valid-token-1234567890", tok)
	assert.Contains(t, out.String(), "tunnel-abcdef12")
}

func TestDefault_ExistingConfigResumesSilently(t *testing.T) {
	reg := &fakeRegistrar{}
	ks := keystore.NewMemoryKeystore()
	require.NoError(t, ks.Set(KeyAPIToken, "saved-token-1234567890"))
	require.NoError(t, ks.Set(KeyTunnelUUID, "existing-uuid"))
	require.NoError(t, ks.Set(KeyAgentName, "tunnel-existing"))
	require.NoError(t, ks.Set(KeyStcpSecret, "existing-secret"))
	require.NoError(t, ks.Set(KeyTransport, "tcp"))

	deps, out, _ := newDeps(reg, ks, nil)
	// No TunnelRunner — resume returns nil after printing the resume banner.
	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, out.String(), "Resuming")
	assert.Contains(t, out.String(), "existing-uuid")
	assert.Equal(t, 0, reg.calls, "resume must not re-register")
}

func TestDefault_FreshInstallNoPromptReturnsNotConfigured(t *testing.T) {
	reg := &fakeRegistrar{}
	ks := keystore.NewMemoryKeystore()

	deps, _, errBuf := newDeps(reg, ks, nil)
	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitNotConfigured, code)
	assert.Contains(t, errBuf.String(), "no saved tunnel configuration")
}

func TestDefault_WizardCancelledReturnsCode130(t *testing.T) {
	reg := &fakeRegistrar{}
	ks := keystore.NewMemoryKeystore()
	prompt := &scriptedPrompt{tokErr: wizard.ErrCancelled}
	deps, _, _ := newDeps(reg, ks, prompt)

	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitCancelled, code)
	assert.Equal(t, 0, reg.calls)
}

func TestStatus_NoSavedConfigPrintsHint(t *testing.T) {
	deps, out, _ := newDeps(&fakeRegistrar{}, keystore.NewMemoryKeystore(), nil)
	code := Run(context.Background(), []string{"status"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, out.String(), "no saved tunnel")
}

func TestStatus_ExistingConfigPrintsRedactedFields(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	_ = ks.Set(KeyTunnelUUID, "existing-uuid")
	_ = ks.Set(KeyAgentName, "tunnel-existing")
	_ = ks.Set(KeyTransport, "quic")
	_ = ks.Set(KeyAPIToken, "saved-token-1234567890")
	_ = ks.Set(KeyStcpSecret, "REDACTED-PLAINTEXT-DO-NOT-LEAK")

	deps, out, _ := newDeps(&fakeRegistrar{}, ks, nil)
	code := Run(context.Background(), []string{"status"}, deps)
	assert.Equal(t, ExitOK, code)
	body := out.String()
	assert.Contains(t, body, "existing-uuid")
	assert.Contains(t, body, "tunnel-existing")
	assert.Contains(t, body, "quic")
	assert.NotContains(t, body, "REDACTED-PLAINTEXT-DO-NOT-LEAK", "status must not print plaintext secret")
	assert.NotContains(t, body, "saved-token-1234567890", "status must not print plaintext token")
}

func TestReset_ClearsKeystoreEntries(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	for _, k := range []string{KeyAPIToken, KeyTunnelUUID, KeyAgentName, KeyStcpSecret, KeyTransport} {
		_ = ks.Set(k, "anything")
	}

	deps, out, _ := newDeps(&fakeRegistrar{}, ks, nil)
	code := Run(context.Background(), []string{"reset"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, out.String(), "cleared")

	for _, k := range []string{KeyAPIToken, KeyTunnelUUID, KeyAgentName, KeyStcpSecret, KeyTransport} {
		_, err := ks.Get(k)
		assert.ErrorIs(t, err, keystore.ErrNotFound, k)
	}
}

func TestReset_ThenDefaultRetriggersWizard(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	_ = ks.Set(KeyAPIToken, "old-token")
	_ = ks.Set(KeyTunnelUUID, "old-uuid")

	prompt := &scriptedPrompt{tokens: []string{"new-token-1234567890"}}
	deps, _, _ := newDeps(reg, ks, prompt)

	code := Run(context.Background(), []string{"reset"}, deps)
	assert.Equal(t, ExitOK, code)

	code = Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, 1, reg.calls, "reset must force the wizard path on next run")

	tok, _ := ks.Get(KeyAPIToken)
	assert.Equal(t, "new-token-1234567890", tok)
}

func TestDiagnose_PlaceholderReturnsZero(t *testing.T) {
	deps, out, _ := newDeps(&fakeRegistrar{}, keystore.NewMemoryKeystore(), nil)
	code := Run(context.Background(), []string{"diagnose"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, strings.ToLower(out.String()), "diagnose")
}

func TestStart_InvokesTunnelRunnerWithRegisteredCredentials(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{}
	deps, _, _ := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitOK, code)

	require.Equal(t, int64(1), runner.calls.Load())
	got := runner.snapshot()[0]
	assert.Equal(t, "valid-token-1234567890", got.Token)
	assert.Equal(t, "tunnel-abcdef12", got.AgentName)
	assert.Equal(t, "s-base64", got.Secret)
	assert.Equal(t, frpc.TransportTCP, got.Transport)
}

func TestStart_QUICTransportPropagatesIntoRunnerInputs(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{}
	deps, _, _ := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890", "--transport", "quic"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, frpc.TransportQUIC, runner.snapshot()[0].Transport)
}

func TestStart_RunnerErrorBubblesUp(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{err: errors.New("frpc launch failed")}
	deps, _, errBuf := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"start", "-a", "valid-token-1234567890"}, deps)
	assert.Equal(t, ExitGeneric, code)
	assert.Contains(t, errBuf.String(), "frpc launch failed")
}

func TestStart_ContextCancelStopsRunnerCleanly(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{block: true}
	deps, _, _ := newDeps(reg, ks, nil)
	deps.TunnelRunner = runner

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ExitCode, 1)
	go func() {
		done <- Run(ctx, []string{"start", "-a", "valid-token-1234567890"}, deps)
	}()

	// Wait for the runner to be invoked, then cancel.
	require.Eventually(t, func() bool { return runner.calls.Load() == 1 }, 2*1e9, 1e7)
	cancel()

	select {
	case code := <-done:
		assert.Equal(t, ExitOK, code, "ctx cancel must produce a clean exit")
	case <-context.Background().Done():
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestDefault_ResumeRoutesThroughRunnerWithSavedCreds(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	require.NoError(t, ks.Set(KeyAPIToken, "saved-token-1234567890"))
	require.NoError(t, ks.Set(KeyTunnelUUID, "saved-uuid"))
	require.NoError(t, ks.Set(KeyAgentName, "tunnel-saved01"))
	require.NoError(t, ks.Set(KeyStcpSecret, "saved-secret"))
	require.NoError(t, ks.Set(KeyTransport, "tcp"))

	runner := &recordingRunner{}
	deps, _, _ := newDeps(&fakeRegistrar{}, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitOK, code)
	require.Equal(t, int64(1), runner.calls.Load())

	got := runner.snapshot()[0]
	assert.Equal(t, "saved-token-1234567890", got.Token)
	assert.Equal(t, "tunnel-saved01", got.AgentName)
	assert.Equal(t, "saved-secret", got.Secret)
	assert.Equal(t, frpc.TransportTCP, got.Transport)
}

func TestDefault_WizardThenRunnerPath(t *testing.T) {
	reg := &fakeRegistrar{results: []registerResult{{tunnel: okTunnel()}}}
	ks := keystore.NewMemoryKeystore()
	prompt := &scriptedPrompt{tokens: []string{"valid-token-1234567890"}}
	runner := &recordingRunner{}
	deps, _, _ := newDeps(reg, ks, prompt)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, int64(1), runner.calls.Load(), "wizard path must invoke the runner once")
}

func TestStatus_DoesNotInvokeRunner(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	runner := &recordingRunner{}
	deps, _, _ := newDeps(&fakeRegistrar{}, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"status"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, int64(0), runner.calls.Load(), "status must not start the tunnel")
}

func TestReset_DoesNotInvokeRunner(t *testing.T) {
	ks := keystore.NewMemoryKeystore()
	_ = ks.Set(KeyAPIToken, "x")
	runner := &recordingRunner{}
	deps, _, _ := newDeps(&fakeRegistrar{}, ks, nil)
	deps.TunnelRunner = runner

	code := Run(context.Background(), []string{"reset"}, deps)
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, int64(0), runner.calls.Load(), "reset must not start the tunnel")
}

func TestRoot_UnknownCommandIsHandledByCobra(t *testing.T) {
	deps, _, errBuf := newDeps(&fakeRegistrar{}, keystore.NewMemoryKeystore(), nil)
	code := Run(context.Background(), []string{"unknown-subcommand"}, deps)
	assert.NotEqual(t, ExitOK, code)
	assert.NotEmpty(t, errBuf.String())
}
