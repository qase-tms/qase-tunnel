package frpc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qase-tms/qase-tunnel/internal/buildinfo"
)

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportQUIC Transport = "quic"

	defaultTCPPort  = 7000
	defaultQUICPort = 7002
)

func serverAddr() string {
	return buildinfo.FrpsServer
}

func serverPortFor(tr Transport) int {
	override := buildinfo.FrpsTCPPort
	def := defaultTCPPort
	if tr == TransportQUIC {
		override = buildinfo.FrpsQUICPort
		def = defaultQUICPort
	}
	if override == "" || override == "0" {
		return def
	}
	parsed, err := parsePort(override)
	if err != nil {
		return def
	}
	return parsed
}

func parsePort(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return n, nil
}

var ErrInvalidTransport = errors.New("invalid transport: must be tcp or quic")

type Inputs struct {
	Token     string
	AgentName string
	Secret    string
	Transport Transport
	Debug     bool
}

func (i Inputs) Validate() error {
	switch i.Transport {
	case TransportTCP, TransportQUIC:
	default:
		return ErrInvalidTransport
	}
	if i.Token == "" {
		return errors.New("token must not be empty")
	}
	if i.AgentName == "" {
		return errors.New("agent_name must not be empty")
	}
	if i.Secret == "" {
		return errors.New("secret must not be empty")
	}
	return nil
}

func Render(in Inputs) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}

	port := serverPortFor(in.Transport)

	var b strings.Builder
	fmt.Fprintf(&b, "serverAddr = %q\n", serverAddr())
	fmt.Fprintf(&b, "serverPort = %d\n", port)
	fmt.Fprintf(&b, "metadatas.token = %q\n", in.Token)
	if in.Debug {
		fmt.Fprintf(&b, "log.level = \"debug\"\n")
	} else {
		fmt.Fprintf(&b, "log.level = \"info\"\n")
	}
	fmt.Fprintf(&b, "transport.protocol = %q\n", string(in.Transport))
	fmt.Fprintf(&b, "transport.tls.enable = false\n")
	fmt.Fprintf(&b, "transport.poolCount = 50\n")
	b.WriteString("\n")
	b.WriteString("[[proxies]]\n")
	fmt.Fprintf(&b, "name = %q\n", in.AgentName)
	fmt.Fprintf(&b, "type = \"stcp\"\n")
	fmt.Fprintf(&b, "secretKey = %q\n", in.Secret)
	fmt.Fprintf(&b, "transport.useEncryption = true\n")
	fmt.Fprintf(&b, "transport.useCompression = true\n")
	b.WriteString("[proxies.plugin]\n")
	fmt.Fprintf(&b, "type = \"http_proxy\"\n")

	return b.String(), nil
}

// WriteTemp writes the rendered TOML to a 0600 temp file and returns its
// path plus a cleanup hook the caller must invoke on shutdown.
func WriteTemp(in Inputs) (path string, cleanup func(), err error) {
	body, err := Render(in)
	if err != nil {
		return "", nil, err
	}

	dir := tempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}

	f, err := os.CreateTemp(dir, "qase-tunnel-*.toml")
	if err != nil {
		return "", nil, err
	}

	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}

	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// Redact replaces the plaintext token and secret with placeholders so the
// rendered config can be safely shared in support bundles.
func Redact(content string, in Inputs) string {
	out := content
	if in.Token != "" {
		out = strings.ReplaceAll(out, in.Token, "[REDACTED-TOKEN]")
	}
	if in.Secret != "" {
		out = strings.ReplaceAll(out, in.Secret, "[REDACTED-SECRET]")
	}
	return out
}

func tempDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "qase-tunnel")
	}
	return filepath.Join(os.TempDir(), "qase-tunnel")
}
