package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qase-tms/qase-tunnel/internal/buildinfo"
)

const defaultBaseURL = "https://api.qase.io"

var (
	ErrUnauthorized      = errors.New("api: token rejected")
	ErrServerUnavailable = errors.New("api: qase server unavailable")
	ErrTunnelRevoked     = errors.New("api: tunnel revoked")
	ErrTunnelNotFound    = errors.New("api: tunnel not found")
)

type Tunnel struct {
	UUID      string `json:"uuid"`
	AgentName string `json:"agent_name"`
	Secret    string `json:"secret"`
}

type TunnelListItem struct {
	UUID       string `json:"uuid"`
	AgentName  string `json:"agent_name"`
	Status     string `json:"status"`
	LastSeenAt string `json:"last_seen_at"`
	CreatedAt  string `json:"created_at"`
}

type tunnelListResponse struct {
	Tunnels []TunnelListItem `json:"tunnels"`
}

type Registrar interface {
	RegisterTunnel(ctx context.Context, token string) (Tunnel, error)
	ListTunnels(ctx context.Context, token string) ([]TunnelListItem, error)
	RotateTunnel(ctx context.Context, token, uuid string) (Tunnel, error)
	Heartbeat(ctx context.Context, token, uuid string) error
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) RegisterTunnel(ctx context.Context, token string) (Tunnel, error) {
	url := c.BaseURL + buildinfo.PathTunnelRegister
	t, err := c.tunnelExchange(ctx, http.MethodPost, url, token, []byte("{}"))
	if errors.Is(err, ErrTunnelNotFound) {
		return Tunnel{}, fmt.Errorf("register route not found at %s", url)
	}
	return t, err
}

func (c *Client) ListTunnels(ctx context.Context, token string) ([]TunnelListItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+buildinfo.PathTunnelsList, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Token", token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
		var out tunnelListResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode tunnel list: %w", err)
		}
		return out.Tunnels, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode >= 500:
		return nil, ErrServerUnavailable
	default:
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

func (c *Client) RotateTunnel(ctx context.Context, token, uuid string) (Tunnel, error) {
	path := fmt.Sprintf(buildinfo.PathTunnelRotateFmt, uuid)
	return c.tunnelExchange(ctx, http.MethodPost, c.BaseURL+path, token, []byte("{}"))
}

func (c *Client) Heartbeat(ctx context.Context, token, uuid string) error {
	path := fmt.Sprintf(buildinfo.PathTunnelHeartbeat, uuid)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusConflict:
		return ErrTunnelRevoked
	case resp.StatusCode == http.StatusNotFound:
		return ErrTunnelNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode >= 500:
		return ErrServerUnavailable
	default:
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

func (c *Client) tunnelExchange(ctx context.Context, method, url, token string, body []byte) (Tunnel, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return Tunnel{}, err
	}
	req.Header.Set("Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Tunnel{}, fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
		var t Tunnel
		if err := json.Unmarshal(respBody, &t); err != nil {
			return Tunnel{}, fmt.Errorf("decode tunnel response: %w", err)
		}
		if t.UUID == "" || t.AgentName == "" || t.Secret == "" {
			return Tunnel{}, fmt.Errorf("malformed tunnel response: missing fields")
		}
		return t, nil
	case resp.StatusCode == http.StatusConflict:
		return Tunnel{}, ErrTunnelRevoked
	case resp.StatusCode == http.StatusNotFound:
		return Tunnel{}, ErrTunnelNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Tunnel{}, ErrUnauthorized
	case resp.StatusCode >= 500:
		return Tunnel{}, ErrServerUnavailable
	default:
		return Tunnel{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}
