package wizard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/qase-tms/qase-tunnel/internal/api"
)

// scriptedPrompt returns answers from queues; one queue per question type.
type scriptedPrompt struct {
	mu      sync.Mutex
	tokens  []string
	tokErrs []error
	retries []bool
	retErrs []error
}

func (p *scriptedPrompt) AskToken(_ context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tokErrs) > 0 {
		err := p.tokErrs[0]
		p.tokErrs = p.tokErrs[1:]
		if err != nil {
			return "", err
		}
	}
	if len(p.tokens) == 0 {
		return "", errors.New("scriptedPrompt: out of token answers")
	}
	t := p.tokens[0]
	p.tokens = p.tokens[1:]
	return t, nil
}

func (p *scriptedPrompt) AskRetry(_ context.Context, _ string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.retErrs) > 0 {
		err := p.retErrs[0]
		p.retErrs = p.retErrs[1:]
		if err != nil {
			return false, err
		}
	}
	if len(p.retries) == 0 {
		return false, errors.New("scriptedPrompt: out of retry answers")
	}
	r := p.retries[0]
	p.retries = p.retries[1:]
	return r, nil
}

type fakeAPI struct {
	mu       sync.Mutex
	calls    int
	results  []apiResult
}

type apiResult struct {
	tunnel api.Tunnel
	err    error
}

func (f *fakeAPI) RegisterTunnel(_ context.Context, _ string) (api.Tunnel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.results) == 0 {
		return api.Tunnel{}, errors.New("fakeAPI: no scripted result")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.tunnel, r.err
}

func (f *fakeAPI) ListTunnels(_ context.Context, _ string) ([]api.TunnelListItem, error) {
	return nil, nil
}

func (f *fakeAPI) RotateTunnel(_ context.Context, _, _ string) (api.Tunnel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return api.Tunnel{}, errors.New("fakeAPI: no scripted result")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.tunnel, r.err
}

func (f *fakeAPI) Heartbeat(_ context.Context, _, _ string) error {
	return nil
}

func TestValidateToken(t *testing.T) {
	cases := []struct {
		raw    string
		errMsg string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"short", "too short"},
		{"valid-token-1234567890", ""},
		{"contains spaces here", "invalid characters"},
		{"contäins-non-ascii-1234", "invalid characters"},
	}
	for _, c := range cases {
		err := ValidateToken(c.raw)
		if c.errMsg == "" {
			assert.NoError(t, err, c.raw)
			continue
		}
		require.Error(t, err, c.raw)
		assert.Contains(t, err.Error(), c.errMsg)
	}
}

func TestWizard_HappyRunReturnsTokenAndTunnel(t *testing.T) {
	p := &scriptedPrompt{tokens: []string{"valid-token-1234567890"}}
	api := &fakeAPI{results: []apiResult{
		{tunnel: api.Tunnel{UUID: "u1", AgentName: "tunnel-abc", Secret: "s1"}},
	}}

	w := &Wizard{Prompt: p, API: api, Out: io.Discard}
	res, err := w.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "valid-token-1234567890", res.Token)
	assert.Equal(t, "u1", res.Tunnel.UUID)
}

func TestWizard_UnauthorizedReprompts(t *testing.T) {
	p := &scriptedPrompt{
		tokens: []string{"first-bad-token-1234", "second-good-token-1234"},
	}
	api := &fakeAPI{results: []apiResult{
		{err: api.ErrUnauthorized},
		{tunnel: api.Tunnel{UUID: "u2", AgentName: "tunnel-xyz", Secret: "s2"}},
	}}

	out := &bytes.Buffer{}
	w := &Wizard{Prompt: p, API: api, Out: out}
	res, err := w.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "second-good-token-1234", res.Token)
	assert.Equal(t, 2, api.calls)
	assert.Contains(t, out.String(), "rejected")
}

func TestWizard_ServerUnavailableLoopsWhenUserAcceptsRetry(t *testing.T) {
	p := &scriptedPrompt{
		tokens:  []string{"tok-1234567890abcd", "tok-1234567890abcd"},
		retries: []bool{true},
	}
	api := &fakeAPI{results: []apiResult{
		{err: api.ErrServerUnavailable},
		{tunnel: api.Tunnel{UUID: "u3", AgentName: "tunnel-xyz", Secret: "s3"}},
	}}

	out := &bytes.Buffer{}
	w := &Wizard{Prompt: p, API: api, Out: out}
	res, err := w.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "u3", res.Tunnel.UUID)
	assert.Contains(t, out.String(), "unavailable")
}

func TestWizard_ServerUnavailableExitsWhenUserDeclinesRetry(t *testing.T) {
	p := &scriptedPrompt{
		tokens:  []string{"tok-1234567890abcd"},
		retries: []bool{false},
	}
	api := &fakeAPI{results: []apiResult{{err: api.ErrServerUnavailable}}}

	w := &Wizard{Prompt: p, API: api, Out: io.Discard}
	_, err := w.Run(context.Background())
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestWizard_SIGINTOnPromptReturnsErrCancelled(t *testing.T) {
	p := &scriptedPrompt{tokErrs: []error{ErrCancelled}}
	api := &fakeAPI{}

	w := &Wizard{Prompt: p, API: api, Out: io.Discard}
	_, err := w.Run(context.Background())
	assert.ErrorIs(t, err, ErrCancelled)
	assert.Equal(t, 0, api.calls, "must not call API after cancel")
}

func TestWizard_EOFOnPromptMapsToCancelled(t *testing.T) {
	p := &scriptedPrompt{tokErrs: []error{io.EOF}}
	w := &Wizard{Prompt: p, API: &fakeAPI{}, Out: io.Discard}
	_, err := w.Run(context.Background())
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestWizard_ContextCancelledMapsToCancelled(t *testing.T) {
	p := &scriptedPrompt{tokErrs: []error{context.Canceled}}
	w := &Wizard{Prompt: p, API: &fakeAPI{}, Out: io.Discard}
	_, err := w.Run(context.Background())
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestWizard_UnknownAPIErrorBubblesUp(t *testing.T) {
	boom := errors.New("network gremlin")
	p := &scriptedPrompt{tokens: []string{"valid-token-1234567890"}}
	api := &fakeAPI{results: []apiResult{{err: boom}}}

	w := &Wizard{Prompt: p, API: api, Out: io.Discard}
	_, err := w.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register tunnel")
}
