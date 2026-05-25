package main

import (
	"context"
	"os"
	"runtime"

	"github.com/qase-tms/qase-tunnel/internal/api"
	"github.com/qase-tms/qase-tunnel/internal/buildinfo"
	"github.com/qase-tms/qase-tunnel/internal/cli"
	"github.com/qase-tms/qase-tunnel/internal/keystore"
	"github.com/qase-tms/qase-tunnel/internal/wizard"
)

func main() {
	ctx, cancel := cli.SignalContext()
	defer cancel()

	ks, err := keystore.NewFileKeystore("")
	if err != nil {
		os.Stderr.WriteString("keystore init: " + err.Error() + "\n")
		os.Exit(int(cli.ExitGeneric))
	}

	deps := cli.Deps{
		APIClient:    api.New(buildinfo.APIBaseURL),
		Keystore:     ks,
		Prompt:       surveyPrompt{},
		TunnelRunner: &cli.FrpcRunner{Binary: defaultFrpcBinary(), Output: os.Stdout},
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	}

	os.Exit(int(cli.Run(ctx, os.Args[1:], deps)))
}

type surveyPrompt struct{}

func (surveyPrompt) AskToken(_ context.Context) (string, error) {
	os.Stdout.WriteString("? Paste your Qase API token (https://app.qase.io/user/api/token):\n> ")
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(tmp)
		if err != nil {
			return "", err
		}
		if n == 0 || tmp[0] == '\n' {
			break
		}
		if tmp[0] == '\r' {
			continue
		}
		buf = append(buf, tmp[0])
	}
	token := string(buf)
	if err := wizard.ValidateToken(token); err != nil {
		os.Stdout.WriteString("✗ " + err.Error() + " — try again.\n")
		return surveyPrompt{}.AskToken(context.Background())
	}
	return token, nil
}

func (surveyPrompt) AskRetry(_ context.Context, _ string) (bool, error) {
	os.Stdout.WriteString("? Try again? [y/N]: ")
	buf := make([]byte, 1)
	if _, err := os.Stdin.Read(buf); err != nil {
		return false, err
	}
	return buf[0] == 'y' || buf[0] == 'Y', nil
}

func defaultFrpcBinary() string {
	name := "frpc"
	if runtime.GOOS == "windows" {
		name = "frpc.exe"
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	bundled := filepathJoin(filepathDir(exe), name)
	if _, err := os.Stat(bundled); err == nil {
		return bundled
	}
	return name
}

func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

func filepathJoin(a, b string) string {
	if a == "" {
		return b
	}
	if a[len(a)-1] == '/' || a[len(a)-1] == '\\' {
		return a + b
	}
	return a + "/" + b
}
