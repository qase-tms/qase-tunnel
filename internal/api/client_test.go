package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/qase-tms/qase-tunnel/internal/buildinfo"
)

func TestClient_RegisterTunnel_HappyReturnsTunnel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, buildinfo.PathTunnelRegister, r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("Token"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid":"u1","agent_name":"tunnel-abc","secret":"s1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	tn, err := c.RegisterTunnel(context.Background(), "test-token")
	require.NoError(t, err)
	assert.Equal(t, "u1", tn.UUID)
	assert.Equal(t, "tunnel-abc", tn.AgentName)
	assert.Equal(t, "s1", tn.Secret)
}

func TestClient_RegisterTunnel_401IsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.RegisterTunnel(context.Background(), "bad")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestClient_RegisterTunnel_403IsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.RegisterTunnel(context.Background(), "bad")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestClient_RegisterTunnel_5xxIsServerUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.RegisterTunnel(context.Background(), "tok")
	assert.ErrorIs(t, err, ErrServerUnavailable)
}

func TestClient_RegisterTunnel_MalformedBodyRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid":"only-uuid"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.RegisterTunnel(context.Background(), "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestClient_DefaultBaseURLApplied(t *testing.T) {
	c := New("")
	assert.Equal(t, defaultBaseURL, c.BaseURL)
}
