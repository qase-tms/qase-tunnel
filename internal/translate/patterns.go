package translate

import (
	"regexp"
	"strings"
)

type Pattern struct {
	Anchor   string
	Match    *regexp.Regexp
	Headline string
	Banner   string
}

type Translation struct {
	Pattern  Pattern
	Original string
}

func (t Translation) Translated() bool { return t.Pattern.Anchor != "" }

const troubleshootingURL = "https://github.com/qase-tms/qase-tunnel/blob/main/TROUBLESHOOTING.md#"

// DefaultPatterns are evaluated in order; first match wins, so more
// specific patterns precede more general ones.
var DefaultPatterns = []Pattern{
	{
		Anchor:   "stcp-secret-mismatch",
		Match:    regexp.MustCompile(`(?i)(stcp.*secret.*invalid|secret.*mismatch|xtcp.*secret)`),
		Headline: "Tunnel secret rotated",
		Banner:   "The tunnel secret has been rotated. Restart `qase-tunnel` to fetch the new secret.",
	},
	{
		Anchor:   "tunnel-revoked",
		Match:    regexp.MustCompile(`(?i)(tunnel\s+(was\s+)?revoked|tunnel\s+is\s+revoked)`),
		Headline: "Tunnel revoked",
		Banner:   "This tunnel was revoked. Run `qase-tunnel reset` and register a new one.",
	},
	{
		Anchor:   "bearfoos-quarantine",
		Match:    regexp.MustCompile(`(?i)(Trojan:Win32/Bearfoos|quarantined.*frpc|defender.*frpc)`),
		Headline: "Defender quarantined the embedded frpc",
		Banner:   "Microsoft Defender flagged the embedded transport. Add an exclusion for the qase-tunnel install directory.",
	},
	{
		Anchor:   "wsarecv",
		Match:    regexp.MustCompile(`(?i)(wsarecv|wsasend|forcibly\s+closed|forçado\s+o\s+cancelamento)`),
		Headline: "Endpoint security is RST'ing the tunnel",
		Banner:   "Your EDR/Antivirus is resetting the tunnel's TLS connection. Add a process exclusion for the qase-tunnel binary.",
	},
	{
		Anchor:   "x509-unknown-authority",
		Match:    regexp.MustCompile(`(?i)x509:\s*certificate\s+signed\s+by\s+unknown\s+authority`),
		Headline: "Internal CA not trusted",
		Banner:   "The relay endpoint's certificate is signed by an authority your machine doesn't trust. Import your internal CA into the system trust store.",
	},
	{
		Anchor:   "no-such-host",
		Match:    regexp.MustCompile(`(?i)(no\s+such\s+host|getaddrinfow:\s*No\s+such\s+host)`),
		Headline: "Hostname did not resolve",
		Banner:   "DNS lookup failed for the tunnel server. Check your DNS configuration and corporate VPN/split-DNS setup.",
	},
	{
		Anchor:   "connection-refused",
		Match:    regexp.MustCompile(`(?i)(connection\s+refused|actively\s+refused|No\s+connection\s+could\s+be\s+made|connectex.*refused)`),
		Headline: "Tunnel server refused the connection",
		Banner:   "The tunnel server refused the connection. Verify the relay address and that outbound traffic to it is allowed.",
	},
	{
		Anchor:   "dial-tcp-timeout",
		Match:    regexp.MustCompile(`(?i)(dial\s+tcp.*i/o\s+timeout|dialTCP:\s*lookup.*timeout|dial\s+tcp.*timeout)`),
		Headline: "Network timeout reaching tunnel server",
		Banner:   "Your network blocks outbound traffic to the tunnel server. Check firewall and proxy rules.",
	},
	{
		Anchor:   "login-failed",
		Match:    regexp.MustCompile(`(?i)(login\s+to\s+server\s+failed|authentication\s+failed|invalid\s+authentication|invalid\s+user\s+token)`),
		Headline: "Authentication rejected",
		Banner:   "The Qase API token is invalid or expired. Generate a new one at https://app.qase.io/user/api/token and run `qase-tunnel reset`.",
	},
}

func Translate(line string) Translation {
	return TranslateWith(DefaultPatterns, line)
}

func TranslateWith(patterns []Pattern, line string) Translation {
	for _, p := range patterns {
		if p.Match.MatchString(line) {
			return Translation{Pattern: p, Original: line}
		}
	}
	return Translation{Original: line}
}

func (t Translation) AnchorURL() string {
	if t.Pattern.Anchor == "" {
		return ""
	}
	return troubleshootingURL + t.Pattern.Anchor
}

func (t Translation) FormatBanner() string {
	if !t.Translated() {
		return t.Original
	}
	var b strings.Builder
	b.WriteString("\n[!] ")
	b.WriteString(t.Pattern.Headline)
	b.WriteString("\n    ")
	b.WriteString(t.Pattern.Banner)
	b.WriteString("\n    See: ")
	b.WriteString(t.AnchorURL())
	b.WriteString("\nOriginal: ")
	b.WriteString(t.Original)
	return b.String()
}
