package wizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/qase-tms/qase-tunnel/internal/api"
)

var ErrCancelled = errors.New("wizard: cancelled by user")

type Prompt interface {
	AskToken(ctx context.Context) (string, error)
	AskRetry(ctx context.Context, message string) (bool, error)
}

type Wizard struct {
	Prompt Prompt
	API    api.Registrar
	Out    io.Writer
}

type Result struct {
	Token  string
	Tunnel api.Tunnel
}

func (w *Wizard) Run(ctx context.Context) (Result, error) {
	for {
		token, err := w.Prompt.AskToken(ctx)
		if err != nil {
			if errors.Is(err, ErrCancelled) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return Result{}, ErrCancelled
			}
			return Result{}, fmt.Errorf("ask token: %w", err)
		}

		tunnel, err := w.API.RegisterTunnel(ctx, token)
		switch {
		case err == nil:
			w.printlnf("✓ Tunnel registered (%s).", tunnel.AgentName)
			return Result{Token: token, Tunnel: tunnel}, nil
		case errors.Is(err, api.ErrUnauthorized):
			w.printlnf("✗ Token rejected — please re-paste.")
			continue
		case errors.Is(err, api.ErrServerUnavailable):
			w.printlnf("⌛ Qase API is currently unavailable.")
			retry, askErr := w.Prompt.AskRetry(ctx, "Try again?")
			if askErr != nil || !retry {
				return Result{}, ErrCancelled
			}
			continue
		default:
			return Result{}, fmt.Errorf("register tunnel: %w", err)
		}
	}
}

func (w *Wizard) printlnf(format string, a ...any) {
	if w.Out == nil {
		return
	}
	fmt.Fprintf(w.Out, format+"\n", a...)
}

func ValidateToken(raw string) error {
	t := strings.TrimSpace(raw)
	if t == "" {
		return errors.New("token must not be empty")
	}
	if len(t) < 16 {
		return errors.New("token is too short")
	}
	for _, r := range t {
		if r > 127 || r == ' ' {
			return errors.New("token contains invalid characters")
		}
	}
	return nil
}
