package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/qase-tms/qase-tunnel/internal/api"
	"github.com/qase-tms/qase-tunnel/internal/keystore"
	"github.com/qase-tms/qase-tunnel/internal/transport/frpc"
	"github.com/qase-tms/qase-tunnel/internal/wizard"
)

const heartbeatInterval = 15 * time.Second

const (
	KeyAPIToken   = "api_token"
	KeyTunnelUUID = "tunnel_uuid"
	KeyAgentName  = "agent_name"
	KeyStcpSecret = "stcp_secret"
	KeyTransport  = "transport"
)

type ExitCode int

const (
	ExitOK            ExitCode = 0
	ExitGeneric       ExitCode = 1
	ExitUsage         ExitCode = 2
	ExitCancelled     ExitCode = 130
	ExitNotConfigured ExitCode = 3
	ExitTunnelRevoked ExitCode = 4
)

type TunnelRunner interface {
	Run(ctx context.Context, inputs frpc.Inputs) error
}

type Deps struct {
	APIClient    api.Registrar
	Keystore     keystore.Keystore
	Spawner      frpc.Spawner
	Prompt       wizard.Prompt
	TunnelRunner TunnelRunner
	Stdout       io.Writer
	Stderr       io.Writer
	Now          func() time.Time
}

func Run(ctx context.Context, args []string, deps Deps) ExitCode {
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}

	root := buildRoot(ctx, &deps)
	root.SetArgs(args)
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true

	if err := root.Execute(); err != nil {
		return classifyError(err, deps.Stderr)
	}
	return ExitOK
}

func classifyError(err error, w io.Writer) ExitCode {
	switch {
	case errors.Is(err, wizard.ErrCancelled):
		return ExitCancelled
	case errors.Is(err, errFlagUsage):
		fmt.Fprintln(w, err.Error())
		return ExitUsage
	case errors.Is(err, errNotConfigured):
		fmt.Fprintln(w, err.Error())
		return ExitNotConfigured
	default:
		fmt.Fprintln(w, "error:", err.Error())
		return ExitGeneric
	}
}

var (
	errFlagUsage     = errors.New("flag usage error")
	errNotConfigured = errors.New("no saved tunnel configuration; run `qase-tunnel start -a <token>` or `qase-tunnel` to set one up")
)

func buildRoot(ctx context.Context, d *Deps) *cobra.Command {
	root := &cobra.Command{
		Use:           "qase-tunnel",
		Short:         "Qase tunnel — expose your private network to Qase cloud workers.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDefault(cmd.Context(), d, debugFlag(cmd))
		},
	}

	root.PersistentFlags().Bool("debug", false, "emit frpc debug logs (per-request work-conn lines)")

	root.AddCommand(newStartCmd(d))
	root.AddCommand(newStatusCmd(d))
	root.AddCommand(newResetCmd(d))
	root.AddCommand(newDiagnoseCmd(d))

	root.SetContext(ctx)
	return root
}

func debugFlag(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	v, err := cmd.Flags().GetBool("debug")
	if err != nil {
		return false
	}
	return v
}

func newStartCmd(d *Deps) *cobra.Command {
	var (
		token     string
		transport string
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Register and run a tunnel non-interactively.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tr, err := parseTransport(transport)
			if err != nil {
				return fmt.Errorf("%w: %v", errFlagUsage, err)
			}
			if token == "" {
				return fmt.Errorf("%w: -a/--api-token is required for `start`", errFlagUsage)
			}
			return runAcquireAndStore(cmd.Context(), d, token, tr, debugFlag(cmd))
		},
	}
	cmd.Flags().StringVarP(&token, "api-token", "a", "", "Qase API token")
	cmd.Flags().StringVar(&transport, "transport", "tcp", "transport protocol: tcp (default) or quic")
	return cmd
}

func newStatusCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the saved tunnel configuration (no plaintext secret).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(d)
		},
	}
}

func newResetCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Forget the saved configuration; next run triggers the wizard.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, k := range []string{KeyAPIToken, KeyTunnelUUID, KeyAgentName, KeyStcpSecret, KeyTransport} {
				_ = d.Keystore.Delete(k)
			}
			fmt.Fprintln(d.Stdout, "Saved configuration cleared.")
			return nil
		},
	}
}

func newDiagnoseCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "Run the embedded diagnostic suite.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(d.Stdout, "diagnose: not yet implemented")
			return nil
		},
	}
}

func runDefault(ctx context.Context, d *Deps, debug bool) error {
	if _, err := d.Keystore.Get(KeyAPIToken); err == nil {
		return runResume(ctx, d, debug)
	}
	if d.Prompt == nil {
		return errNotConfigured
	}

	w := &wizard.Wizard{Prompt: d.Prompt, API: d.APIClient, Out: d.Stdout}
	res, err := w.Run(ctx)
	if err != nil {
		return err
	}

	tr := frpc.TransportTCP
	if err := persist(d, res.Token, res.Tunnel, tr); err != nil {
		return err
	}
	return runTunnelLoop(ctx, d, res.Token, res.Tunnel.UUID, res.Tunnel.AgentName, res.Tunnel.Secret, tr, debug)
}

func runAcquireAndStore(ctx context.Context, d *Deps, token string, tr frpc.Transport, debug bool) error {
	tunnel, err := acquireTunnel(ctx, d, token)
	if err != nil {
		return err
	}
	if err := persist(d, token, tunnel, tr); err != nil {
		return err
	}
	return runTunnelLoop(ctx, d, token, tunnel.UUID, tunnel.AgentName, tunnel.Secret, tr, debug)
}

func acquireTunnel(ctx context.Context, d *Deps, token string) (api.Tunnel, error) {
	storedUUID, _ := d.Keystore.Get(KeyTunnelUUID)
	if storedUUID == "" {
		return d.APIClient.RegisterTunnel(ctx, token)
	}

	tunnels, err := d.APIClient.ListTunnels(ctx, token)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrServerUnavailable) {
			return api.Tunnel{}, err
		}
		return d.APIClient.RegisterTunnel(ctx, token)
	}

	for _, t := range tunnels {
		if t.UUID != storedUUID {
			continue
		}
		if strings.EqualFold(t.Status, "revoked") {
			break
		}
		return d.APIClient.RotateTunnel(ctx, token, storedUUID)
	}

	return d.APIClient.RegisterTunnel(ctx, token)
}

func runResume(ctx context.Context, d *Deps, debug bool) error {
	uuid, err := d.Keystore.Get(KeyTunnelUUID)
	if err != nil {
		return errNotConfigured
	}
	token, err := d.Keystore.Get(KeyAPIToken)
	if err != nil {
		return errNotConfigured
	}
	agent, err := d.Keystore.Get(KeyAgentName)
	if err != nil {
		return errNotConfigured
	}
	secret, err := d.Keystore.Get(KeyStcpSecret)
	if err != nil {
		return errNotConfigured
	}
	transportRaw, _ := d.Keystore.Get(KeyTransport)
	tr, err := parseTransport(transportRaw)
	if err != nil {
		tr = frpc.TransportTCP
	}

	fmt.Fprintf(d.Stdout, "Resuming saved tunnel %s\n", uuid)
	return runTunnelLoop(ctx, d, token, uuid, agent, secret, tr, debug)
}

func runTunnelLoop(ctx context.Context, d *Deps, token, tunnelUUID, agentName, secret string, tr frpc.Transport, debug bool) error {
	if d.TunnelRunner == nil {
		return nil
	}

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()

	var wg sync.WaitGroup
	if d.APIClient != nil && tunnelUUID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			heartbeatLoop(hbCtx, d.APIClient, token, tunnelUUID)
		}()
	}

	err := d.TunnelRunner.Run(ctx, frpc.Inputs{
		Token:     token,
		AgentName: agentName,
		Secret:    secret,
		Transport: tr,
		Debug:     debug,
	})

	hbCancel()
	wg.Wait()
	return err
}

func heartbeatLoop(ctx context.Context, c api.Registrar, token, uuid string) {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Heartbeat(ctx, token, uuid); err != nil {
				if errors.Is(err, api.ErrTunnelRevoked) {
					log.Printf("tunnel revoked; stopping heartbeat: %v", err)
					return
				}
				log.Printf("heartbeat failed; retry next tick: %v", err)
			}
		}
	}
}

func runStatus(d *Deps) error {
	uuid, err := d.Keystore.Get(KeyTunnelUUID)
	if err != nil {
		fmt.Fprintln(d.Stdout, "no saved tunnel — run `qase-tunnel start -a <token>` to create one")
		return nil
	}
	agent, _ := d.Keystore.Get(KeyAgentName)
	transport, _ := d.Keystore.Get(KeyTransport)
	fmt.Fprintf(d.Stdout, "tunnel_uuid: %s\nagent_name: %s\ntransport: %s\n", uuid, agent, transport)
	return nil
}

func persist(d *Deps, token string, t api.Tunnel, tr frpc.Transport) error {
	for _, kv := range [...]struct{ k, v string }{
		{KeyAPIToken, token},
		{KeyTunnelUUID, t.UUID},
		{KeyAgentName, t.AgentName},
		{KeyStcpSecret, t.Secret},
		{KeyTransport, string(tr)},
	} {
		if err := d.Keystore.Set(kv.k, kv.v); err != nil {
			return fmt.Errorf("save %s: %w", kv.k, err)
		}
	}
	printReadyBanner(d.Stdout, t, tr)
	return nil
}

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiCyan  = "\x1b[36m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
)

func styled(w io.Writer, code, text string) string {
	if !useColor(w) {
		return text
	}
	return code + text + ansiReset
}

func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func printReadyBanner(w io.Writer, t api.Tunnel, tr frpc.Transport) {
	name := styled(w, ansiBold+ansiCyan, t.AgentName)
	check := styled(w, ansiGreen, "✓")
	label := func(s string) string { return styled(w, ansiDim, s) }

	fmt.Fprintf(w, "\n%s Tunnel ready\n\n", check)
	fmt.Fprintf(w, "  %s   %s\n", label("Name     "), name)
	fmt.Fprintf(w, "  %s   %s\n", label("UUID     "), t.UUID)
	fmt.Fprintf(w, "  %s   %s\n", label("Transport"), tr)
	fmt.Fprintf(w, "\n%s Next step\n", styled(w, ansiBold, "→"))
	fmt.Fprintln(w, "  Open Qase → Project Settings → Environments, edit the environment")
	fmt.Fprintf(w, "  you want to route through this tunnel, then select %s in the\n", styled(w, ansiBold, "\""+t.AgentName+"\""))
	fmt.Fprintln(w, "  \"Qase tunnel\" dropdown. Aiden will use the tunnel only for runs")
	fmt.Fprintln(w, "  against environments where it's selected.")
	fmt.Fprintf(w, "\n%s Keep this process running while you test. Press Ctrl+C to stop.\n\n",
		styled(w, ansiDim, "•"))
}

func parseTransport(raw string) (frpc.Transport, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tcp":
		return frpc.TransportTCP, nil
	case "quic":
		return frpc.TransportQUIC, nil
	default:
		return "", fmt.Errorf("unknown transport %q (must be tcp or quic)", raw)
	}
}

func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
